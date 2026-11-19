use super::{App, Kind, Menu, State, clean, colors};
use crate::{protocol::VERSION, ui::tr};
use ratatui::{
    Frame,
    style::{Color, Modifier, Style},
};
use unicode_segmentation::UnicodeSegmentation;

#[derive(Clone, Copy)]
pub(super) struct Palette {
    pub(super) color: bool,
}
impl Palette {
    pub(super) fn current() -> Self {
        let color = std::env::var_os("NO_COLOR").is_none()
            && std::env::var("TERM").unwrap_or_default() != "dumb";
        Self { color }
    }
    fn text(self, role: u32) -> Style {
        if self.color {
            Style::default().fg(Color::from_u32(role))
        } else {
            Style::default()
        }
    }
    fn kind(self, kind: Kind) -> Style {
        match kind {
            Kind::System => self.text(colors::SECONDARY),
            Kind::Command | Kind::Welcome => self.text(colors::PRIMARY),
            Kind::Error => self.text(colors::ERROR),
            Kind::Connection | Kind::Output => Style::default(),
        }
    }
    fn status(self, state: State) -> Style {
        match state {
            State::Connected => self.text(colors::SUCCESS),
            State::Connecting | State::Reconnecting(_) => self.text(colors::WARNING),
            _ => self.text(colors::SECONDARY),
        }
    }
    fn selection(self) -> Style {
        if self.color {
            Style::default()
                .fg(Color::from_u32(colors::ON_SELECTION))
                .bg(Color::from_u32(colors::SELECTION))
        } else {
            Style::default().add_modifier(Modifier::REVERSED)
        }
    }
}
impl App {
    fn status(&self) -> String {
        match self.state {
            State::Disconnected | State::Disconnecting => tr("disconnected.2").into(),
            State::Connecting => tr(if self.progress {
                "3.4.connecting"
            } else {
                "connecting"
            })
            .into(),
            State::Connected => tr(if self.temporary {
                "connected.temporary"
            } else {
                "connected.persistent"
            })
            .into(),
            State::Reconnecting(delay) => {
                tr("connection.retry.status").replace("%d", &delay.to_string())
            }
        }
    }
    pub(super) fn header(&self) -> String {
        let width = usize::from(self.width);
        let status = self.status();
        let left = format!(" Agenterm v{VERSION}");
        if width < self.metrics.width(&left) + 1 + self.metrics.width(&status) {
            return self.metrics.truncate(&status, width, "");
        }
        let mut right = status.clone();
        let available = width - self.metrics.width(&left) - 2;
        if self.state != State::Disconnected && !self.address.is_empty() {
            let address_width = available.saturating_sub(self.metrics.width(&status) + 5);
            if address_width > 0 {
                let address = if let Some((base, id)) = self.address.rsplit_once("/client/") {
                    let suffix = format!("/{id}");
                    if address_width > self.metrics.width(&suffix) {
                        self.metrics.truncate(
                            &format!("{base}/client"),
                            address_width - self.metrics.width(&suffix),
                            "…",
                        ) + &suffix
                    } else {
                        self.metrics.truncate(id, address_width, "…")
                    }
                } else {
                    self.metrics.truncate(&self.address, address_width, "…")
                };
                right = format!("{address}  ·  {status}");
            }
        }
        let gap = width
            .saturating_sub(self.metrics.width(&left) + self.metrics.width(&right) + 1)
            .max(1);
        format!("{left}{}{right} ", " ".repeat(gap))
    }
    fn footer(&self) -> String {
        let width = self.body_width();
        let mut left = if self.state == State::Disconnected {
            tr(if matches!(self.menu, Menu::Mode(_)) {
                "connection.mode.help"
            } else {
                "enter.connect.ctrl.c.quit"
            })
            .to_string()
        } else {
            tr("tui.ctrl.c.quit").to_string()
        };
        if let Some(s) = &self.selection
            && s.dragged
        {
            left = tr("tui.selection.help").into();
            if !s.notice.is_empty() {
                left = format!("{} · {left}", tr(s.notice));
            }
        }
        if self.state == State::Disconnected {
            return self
                .metrics
                .truncate(&left, usize::from(self.width.saturating_sub(1)), "");
        }
        let mut right = tr("tui.command.count").replace("%d", &self.log.completed.to_string());
        if self.state == State::Connected
            && let Some(ms) = self.latency
        {
            right += &format!(" · {}ms", ms.max(1));
        }
        if self.metrics.width(&left) + self.metrics.width(&right) + 1 > width {
            left = self.metrics.truncate(
                &left,
                width.saturating_sub(self.metrics.width(&right) + 1),
                "…",
            );
        }
        let gap = width
            .saturating_sub(self.metrics.width(&left) + self.metrics.width(&right))
            .max(1);
        format!("{left}{}{right}", " ".repeat(gap))
    }
    pub(super) fn render(&self, frame: &mut Frame<'_>) {
        let area = frame.area();
        let palette = self.palette;
        let default = Style::default();
        let header = self.header();
        self.metrics
            .draw(frame.buffer_mut(), 0, 0, area.width, &header, default);
        let status = self.status();
        let status_x = header
            .rfind(&status)
            .map_or(0, |byte| self.metrics.width(&header[..byte]));
        self.metrics.draw(
            frame.buffer_mut(),
            status_x as u16,
            0,
            area.width.saturating_sub(status_x as u16),
            &status,
            palette.status(self.state),
        );
        let divider = "─".repeat(self.body_width());
        if area.height > 1 {
            self.metrics.draw(
                frame.buffer_mut(),
                1,
                1,
                area.width.saturating_sub(2),
                &divider,
                default,
            );
        }
        if area.height >= 5 {
            let width = usize::from(area.width.saturating_sub(2));
            let label = self
                .metrics
                .truncate(&self.identity, width.saturating_sub(2), "…");
            let footer_divider = if self.state == State::Connected && !label.is_empty() {
                format!(
                    "{} {label}",
                    "─".repeat(width - self.metrics.width(&label) - 1)
                )
            } else {
                divider
            };
            self.metrics.draw(
                frame.buffer_mut(),
                1,
                area.height - 3,
                area.width.saturating_sub(2),
                &footer_divider,
                default,
            );
            self.metrics.draw(
                frame.buffer_mut(),
                1,
                area.height - 2,
                area.width.saturating_sub(1),
                &self.footer(),
                default,
            );
        }
        for (i, row) in self
            .log
            .rows
            .iter()
            .skip(self.scroll)
            .take(self.log_height())
            .enumerate()
        {
            let y = 2 + i as u16;
            if y >= area.height.saturating_sub(3) {
                break;
            }
            let columns = self.columns(self.scroll + i);
            let mut x = 0;
            for (byte, g) in row.text.grapheme_indices(true) {
                let width = self.metrics.cluster(g);
                if x + width > usize::from(area.width) {
                    break;
                }
                let selected = columns.is_some_and(|(left, right)| x >= left && x + width <= right);
                let style = if selected {
                    palette.selection()
                } else {
                    palette.kind(row.kind_at(byte))
                };
                self.metrics
                    .draw(frame.buffer_mut(), x as u16, y, width as u16, g, style);
                x += width;
            }
        }
        if self.state == State::Disconnected && self.height >= 6 {
            self.render_form(frame);
        }
    }
    pub(super) fn connection_form(&self) -> Form {
        let palette = self.palette;
        let default = Style::default();
        let mut form = Form::new(self.body_width(), self.metrics);
        form.line(vec![(format!(" {}", tr("connection.token")), default)]);
        let available = self.body_width().saturating_sub(4).max(1);
        let cursor = self.metrics.width(&self.input.value[..self.input.cursor]);
        let mut left = cursor.saturating_sub(available);
        if left > 0 {
            left = self.metrics.snap(&self.input.value, left - 1, true);
        }
        let visible = self.metrics.cut(&self.input.value, left, left + available);
        let (value, style) = if self.input.value.is_empty() {
            (
                tr("paste.connection.token")
                    .chars()
                    .take(available + 1)
                    .collect::<String>(),
                palette.kind(Kind::System),
            )
        } else {
            (visible, default)
        };
        let input_cursor = if self.menu == Menu::Input {
            Some(3 + cursor.saturating_sub(left))
        } else {
            None
        };
        form.input_line(
            vec![
                (" > ".into(), default),
                (value, style),
                (" ".into(), default),
            ],
            input_cursor,
        );
        match self.menu {
            Menu::Mode(selected) => {
                form.blank();
                form.line(vec![(
                    format!(" {}", tr("connection.mode.select")),
                    default,
                )]);
                let labels: Vec<String> = [
                    tr("connection.mode.temporary"),
                    tr("connection.mode.persistent"),
                ]
                .iter()
                .enumerate()
                .map(|(i, s)| {
                    if i == selected {
                        format!(" ▶ {s} ")
                    } else {
                        format!("   {s} ")
                    }
                })
                .collect();
                let horizontal = self.metrics.width(&labels.join("   ")) < self.body_width();
                let styles: Vec<Style> = (0..2)
                    .map(|i| {
                        if i == selected {
                            palette.kind(Kind::Command).add_modifier(Modifier::REVERSED)
                        } else {
                            default
                        }
                    })
                    .collect();
                if horizontal {
                    form.line(vec![
                        (" ".into(), default),
                        (labels[0].clone(), styles[0]),
                        ("   ".into(), default),
                        (labels[1].clone(), styles[1]),
                    ]);
                } else {
                    for i in 0..2 {
                        form.line(vec![(" ".into(), default), (labels[i].clone(), styles[i])]);
                    }
                }
            }
            Menu::Server(selected) => {
                form.blank();
                form.line(vec![(format!(" {}", tr("select.history.server")), default)]);
                let count = self.body_height().saturating_sub(5).max(1);
                let start = (selected + 1).saturating_sub(count);
                for (i, server) in self.servers.iter().enumerate().skip(start).take(count) {
                    form.line(vec![(
                        format!(
                            " {}{}",
                            if i == selected { "> " } else { "  " },
                            clean(server)
                        ),
                        default,
                    )]);
                }
                form.line(vec![(
                    format!(" {}", tr("history.server.select.help")),
                    default,
                )]);
            }
            _ => {}
        }
        if !self.message.is_empty() {
            form.blank();
            form.line(vec![
                (" ".into(), default),
                (self.message.clone(), palette.kind(Kind::Error)),
            ]);
        }
        form
    }
    fn render_form(&self, frame: &mut Frame<'_>) {
        let top = self.log_height();
        let available = self.body_height().saturating_sub(top);
        for (row, atoms) in self
            .connection_form()
            .rows
            .into_iter()
            .take(available)
            .enumerate()
        {
            let y = 2 + (top + row) as u16;
            let mut x = 0;
            for atom in atoms {
                if atom.cursor && self.menu == Menu::Input && self.width > 1 {
                    frame.set_cursor_position((x as u16, y));
                }
                self.metrics.draw(
                    frame.buffer_mut(),
                    x as u16,
                    y,
                    atom.width as u16,
                    &atom.text,
                    atom.style,
                );
                x += atom.width;
            }
        }
    }
}

struct Atom {
    text: String,
    style: Style,
    width: usize,
    cursor: bool,
}
pub(super) struct Form {
    rows: Vec<Vec<Atom>>,
    width: usize,
    metrics: super::Metrics,
}
impl Form {
    pub(super) fn height(&self) -> usize {
        self.rows.len()
    }
    fn new(width: usize, metrics: super::Metrics) -> Self {
        Self {
            rows: vec![],
            width,
            metrics,
        }
    }
    fn blank(&mut self) {
        self.rows.push(vec![]);
    }
    fn line(&mut self, parts: Vec<(String, Style)>) {
        self.input_line(parts, None);
    }
    fn input_line(&mut self, parts: Vec<(String, Style)>, cursor: Option<usize>) {
        let mut atoms = Vec::new();
        let mut col = 0;
        for (text, style) in parts {
            for g in text.graphemes(true) {
                let width = self.metrics.cluster(g);
                atoms.push(Atom {
                    text: g.into(),
                    style,
                    width,
                    cursor: cursor == Some(col),
                });
                col += width;
            }
        }
        let mut wrap = Wrap::new();
        for mut atom in atoms {
            if atom.width > self.width {
                atom.text = "…".into();
                atom.width = 1;
            }
            let ch = atom.text.chars().next().unwrap_or(' ');
            if ch.is_whitespace() && ch != '\u{a0}' {
                wrap.add_word();
                wrap.space_width += atom.width;
                wrap.spaces.push(atom);
            } else if ch == '-' {
                wrap.add_spaces();
                if wrap.column + wrap.word_width >= self.width {
                    wrap.word_width += atom.width;
                    wrap.word.push(atom);
                } else {
                    wrap.add_word();
                    wrap.column += atom.width;
                    wrap.row.push(atom);
                }
            } else if ch.is_ascii() {
                if wrap.column == self.width {
                    wrap.newline();
                }
                wrap.word_width += atom.width;
                wrap.word.push(atom);
                if wrap.word_width == self.width {
                    wrap.add_word();
                }
                if wrap.column + wrap.word_width + wrap.space_width > self.width {
                    wrap.newline();
                }
            } else {
                if wrap.word_width + atom.width > self.width {
                    wrap.add_word();
                }
                wrap.word_width += atom.width;
                wrap.word.push(atom);
                if wrap.column + wrap.word_width + wrap.space_width > self.width {
                    wrap.newline();
                }
            }
        }
        wrap.add_word();
        for atom in wrap.spaces {
            if wrap.column + atom.width > self.width {
                break;
            }
            wrap.column += atom.width;
            wrap.row.push(atom);
        }
        wrap.rows.push(wrap.row);
        self.rows.extend(wrap.rows);
    }
}
// Match Go Lip Gloss / ansi.Wrap, including hyphen breaks and its distinct
// ASCII and wide-grapheme overflow rules. This keeps narrow forms identical.
struct Wrap {
    rows: Vec<Vec<Atom>>,
    row: Vec<Atom>,
    word: Vec<Atom>,
    spaces: Vec<Atom>,
    column: usize,
    word_width: usize,
    space_width: usize,
}
impl Wrap {
    fn new() -> Self {
        Self {
            rows: vec![],
            row: vec![],
            word: vec![],
            spaces: vec![],
            column: 0,
            word_width: 0,
            space_width: 0,
        }
    }
    fn add_spaces(&mut self) {
        self.column += self.space_width;
        self.row.append(&mut self.spaces);
        self.space_width = 0;
    }
    fn add_word(&mut self) {
        if self.word.is_empty() {
            return;
        }
        self.add_spaces();
        self.column += self.word_width;
        self.row.append(&mut self.word);
        self.word_width = 0;
    }
    fn newline(&mut self) {
        self.rows.push(std::mem::take(&mut self.row));
        self.column = 0;
        self.spaces.clear();
        self.space_width = 0;
    }
}
