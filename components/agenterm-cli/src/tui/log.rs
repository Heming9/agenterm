//! Go logview spacing, folding and selection coordinates, independent of rendering.
pub(super) mod welcome;

use super::text::Metrics;
use crate::ui::{self, Event};
use std::collections::{HashMap, VecDeque};
use unicode_segmentation::UnicodeSegmentation;

pub const MAX_LOG: usize = 10 * 1024 * 1024;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    System,
    Command,
    Output,
    Error,
    Welcome,
    Ready,
}
#[derive(Clone, Debug)]
pub struct Row {
    pub text: String,
    pub kind: Kind,
    pub prefix: usize,
    pub marker: usize,
    pub suffix: usize,
    suffix_kind: Kind,
    pub soft: bool,
    pub fold: Option<u64>,
}
impl Row {
    fn blank() -> Self {
        Self {
            text: String::new(),
            kind: Kind::System,
            prefix: 0,
            marker: 0,
            suffix: usize::MAX,
            suffix_kind: Kind::System,
            soft: false,
            fold: None,
        }
    }
    pub fn kind_at(&self, byte: usize) -> Kind {
        if byte < self.marker {
            Kind::Output
        } else if byte < self.prefix {
            Kind::System
        } else if byte >= self.suffix {
            self.suffix_kind
        } else {
            self.kind
        }
    }
}
struct Span {
    id: u64,
    kind: Kind,
    text: String,
    prefix: usize,
    expanded: bool,
    cached: Option<(usize, Metrics, Vec<Row>)>,
}
#[derive(Default)]
pub struct Decoder {
    state: u8,
    started: bool,
    line_content: bool,
    pending: String,
}
impl Decoder {
    pub fn text(&mut self, s: &str) -> String {
        let mut out = String::new();
        for c in s.chars() {
            match self.state {
                1 => {
                    self.state = match c {
                        '[' => 2,
                        ']' => 3,
                        'P' | 'X' | '^' | '_' => 3,
                        _ => 0,
                    }
                }
                2 => {
                    if ('\u{40}'..='\u{7e}').contains(&c) {
                        self.state = 0;
                    }
                }
                3 => {
                    if c == '\x07' {
                        self.state = 0;
                    } else if c == '\x1b' {
                        self.state = 4;
                    }
                }
                4 => self.state = if c == '\\' { 0 } else { 3 },
                _ => {
                    if c == '\x1b' {
                        self.state = 1;
                    } else if c == '\n' || c == '\t' || !c.is_control() {
                        out.push(c);
                    }
                }
            }
        }
        out
    }
    fn output(&mut self, s: &str) -> String {
        let text = self.text(s);
        let mut out = String::new();
        for c in text.chars() {
            if c == '\n' && self.line_content {
                out.push(c);
                self.line_content = false;
            } else if c == '\n' && !self.started {
                self.pending.clear();
            } else if c == '\n' || (!self.line_content && (c == ' ' || c == '\t')) {
                if self.pending.len() < 64 * 1024 {
                    self.pending.push(c);
                }
            } else {
                out.push_str(&std::mem::take(&mut self.pending));
                out.push(c);
                self.started = true;
                self.line_content = true;
            }
        }
        out
    }
}
pub fn clean(s: &str) -> String {
    Decoder::default().text(s)
}

#[derive(Default)]
pub struct Log {
    spans: VecDeque<Span>,
    bytes: usize,
    next_id: u64,
    decoders: HashMap<String, Decoder>,
    last_id: String,
    pub rows: Vec<Row>,
    pub dirty: bool,
    pub evicted: bool,
    pub completed: usize,
    pub welcome_height: Option<usize>,
    pub welcome_rows: usize,
    layout: Option<(usize, Metrics)>,
}
impl Log {
    fn before(&self, n: usize) -> String {
        if self.spans.is_empty() {
            return String::new();
        }
        let tail = self
            .spans
            .iter()
            .rev()
            .flat_map(|s| s.text.chars().rev())
            .take_while(|c| *c == '\n')
            .count();
        "\n".repeat(n.saturating_sub(tail))
    }
    fn push(&mut self, kind: Kind, text: String, prefix: usize) {
        if text.is_empty() {
            return;
        }
        self.bytes += text.len();
        if kind != Kind::System
            && let Some(last) = self.spans.back_mut()
            && last.kind == kind
            && prefix == 0
        {
            last.text.push_str(&text);
            last.cached = None;
        } else {
            self.next_id += 1;
            self.spans.push_back(Span {
                id: self.next_id,
                kind,
                text,
                prefix,
                expanded: false,
                cached: None,
            });
        }
        while self.bytes > MAX_LOG {
            self.evicted = true;
            let excess = self.bytes - MAX_LOG;
            let first = self.spans.front_mut().unwrap();
            if first.text.len() <= excess {
                self.bytes -= first.text.len();
                self.spans.pop_front();
            } else {
                let mut at = excess;
                while !first.text.is_char_boundary(at) {
                    at += 1;
                }
                first.text = first.text[at..].into();
                self.bytes -= at;
                first.prefix = first.prefix.saturating_sub(at);
                first.cached = None;
            }
        }
        self.dirty = true;
    }
    pub fn message(&mut self, kind: Kind, text: &str) {
        let text = format!("{}{}\n", self.before(2), clean(text));
        self.push(kind, text, 0);
    }
    pub fn connection_line(&mut self, kind: Kind, time: &str, message: &str) {
        let prefix = format!("{}{time}  ", self.before(1));
        self.push(kind, format!("{prefix}{}\n", clean(message)), prefix.len());
    }
    pub fn clear_waiting(&mut self) {
        if let Some(index) = self.spans.iter().rposition(|span| span.kind == Kind::Ready)
            && let Some(span) = self.spans.remove(index)
        {
            self.bytes -= span.text.len();
            self.dirty = true;
        }
    }
    pub fn event(&mut self, event: Event) {
        match event {
            Event::Started { id, command, time } => {
                let prefix = format!("{}{} $ ", self.before(2), clean(&time));
                let n = prefix.len();
                self.push(Kind::Command, format!("{prefix}{}\n", clean(&command)), n);
                self.decoders.insert(id.clone(), Decoder::default());
                self.last_id = id;
            }
            Event::Output { id, data } => {
                if let Some(d) = self.decoders.get_mut(&id) {
                    let mut text = d.output(&data);
                    if self.last_id != id {
                        while let Some((line, rest)) = text.split_once('\n') {
                            if !line.trim_matches([' ', '\t']).is_empty() {
                                break;
                            }
                            text = rest.into();
                        }
                    }
                    if !text.is_empty() {
                        if self.last_id != id {
                            let space = self.before(2);
                            self.push(Kind::System, space, 0);
                            self.last_id = id;
                        }
                        self.push(Kind::Output, text, 0);
                    }
                }
            }
            Event::Finished { id, code } => {
                if self.decoders.remove(&id).is_some() {
                    self.completed += 1;
                }
                let _ = code; // Completion is reported in the counter.
            }
            _ => {}
        }
    }
    pub fn refresh(
        &mut self,
        width: usize,
        metrics: Metrics,
        frozen: bool,
        welcome_height: Option<usize>,
    ) {
        let width = width.max(1);
        let resized = self.layout != Some((width, metrics));
        let welcome_changed = self.welcome_height != welcome_height;
        if frozen && !self.evicted && !resized {
            return;
        }
        if !self.dirty && !resized && !welcome_changed {
            return;
        }
        self.rows =
            welcome_height.map_or_else(Vec::new, |height| welcome::rows(width, height, metrics));
        self.welcome_rows = self.rows.len();
        self.rows.push(Row::blank());
        for span in &mut self.spans {
            if resized || span.cached.is_none() {
                span.cached = Some((width, metrics, layout(span, width, metrics)));
            }
            let mut rows = span.cached.as_ref().unwrap().2.iter().cloned();
            if let Some(first) = rows.next() {
                let last = self.rows.last_mut().unwrap();
                if last.text.is_empty() {
                    *last = first;
                } else if !first.text.is_empty() {
                    last.text.push_str(&first.text);
                }
            }
            self.rows.extend(rows);
        }
        self.welcome_height = welcome_height;
        self.layout = Some((width, metrics));
        self.dirty = false;
        self.evicted = false;
    }
    pub fn fold_start(&self, row: usize) -> Option<usize> {
        let id = self.rows.get(row)?.fold?;
        self.rows.iter().position(|r| r.fold == Some(id))
    }
    pub fn toggle(&mut self, row: usize) -> bool {
        let Some(id) = self.rows.get(row).and_then(|r| r.fold) else {
            return false;
        };
        if let Some(span) = self.spans.iter_mut().find(|s| s.id == id) {
            span.expanded = !span.expanded;
            span.cached = None;
            self.dirty = true;
            return true;
        }
        false
    }
}
fn layout(span: &Span, width: usize, metrics: Metrics) -> Vec<Row> {
    let text = span.text.replace('\t', "    ");
    let leading = text.len() - text.trim_start_matches('\n').len();
    let trailing = text.len() - text.trim_end_matches('\n').len();
    let body = text.trim_matches('\n');
    if body.is_empty() {
        return (0..=text.chars().filter(|c| *c == '\n').count())
            .map(|_| Row::blank())
            .collect();
    }
    let mut body_rows = Vec::new();
    let mut offset = leading;
    for line in body.split('\n') {
        let mut row = Row {
            kind: span.kind,
            ..Row::blank()
        };
        let mut col = 0;
        for (byte, g) in line.grapheme_indices(true) {
            let mut g = g;
            let mut n = metrics.cluster(g);
            if n > width {
                g = "…";
                n = 1;
            }
            if col + n > width {
                row.soft = true;
                body_rows.push(row);
                row = Row {
                    kind: span.kind,
                    ..Row::blank()
                };
                col = 0;
            }
            row.text.push_str(g);
            if offset + byte < span.prefix {
                row.prefix = row.text.len();
            }
            col += n;
        }
        body_rows.push(row);
        offset += line.len() + 1;
    }
    let fold =
        matches!(span.kind, Kind::Command | Kind::Output | Kind::Error) && body_rows.len() > 1;
    if fold && !span.expanded {
        let mut suffix = ui::tr("tui.fold.lines").replace("%d", &body_rows.len().to_string());
        suffix.insert(0, ' ');
        if metrics.width(&suffix) + 4 > width {
            suffix.clear();
        }
        let marker = metrics.truncate("▸ ", width, "");
        let available = width.saturating_sub(metrics.width(&marker) + metrics.width(&suffix));
        let first = &body_rows[0];
        let preview = metrics.truncate(&first.text, available, "…");
        let prefix = marker.len() + first.prefix.min(preview.len());
        let suffix_start = marker.len() + preview.len();
        body_rows = vec![Row {
            text: format!("{marker}{preview}{suffix}"),
            kind: span.kind,
            prefix,
            marker: marker.len(),
            suffix: suffix_start,
            fold: Some(span.id),
            ..Row::blank()
        }];
    } else if fold {
        // Reserve the marker before wrapping so no text is lost at the right edge.
        let marked = Span {
            id: span.id,
            kind: span.kind,
            text: format!("▾ {body}"),
            prefix: span.prefix.saturating_sub(leading) + "▾ ".len(),
            expanded: true,
            cached: None,
        };
        let mut rows = wrap_expanded(&marked, width, metrics);
        for r in &mut rows {
            r.fold = Some(span.id);
        }
        body_rows = rows;
    }
    let mut rows: Vec<Row> = (0..leading).map(|_| Row::blank()).collect();
    rows.extend(body_rows);
    rows.extend((0..trailing).map(|_| Row::blank()));
    rows
}
fn wrap_expanded(span: &Span, width: usize, metrics: Metrics) -> Vec<Row> {
    // System disables folding while using the same hard-wrapping implementation.
    let plain = Span {
        id: span.id,
        kind: Kind::System,
        text: span.text.clone(),
        prefix: span.prefix,
        expanded: false,
        cached: None,
    };
    let mut rows = layout(&plain, width, metrics);
    let mut marker = "▾ ".len();
    for row in &mut rows {
        row.marker = marker.min(row.text.len());
        marker = marker.saturating_sub(row.text.len());
        row.kind = span.kind;
    }
    rows
}
