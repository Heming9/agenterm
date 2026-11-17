use ratatui::{
    buffer::{Buffer, CellDiffOption},
    style::Style,
};
use std::num::NonZeroU16;
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Metrics {
    pub legacy: bool,
}
impl Metrics {
    pub fn cluster(self, s: &str) -> usize {
        if !self.legacy {
            return UnicodeWidthStr::width(s);
        }
        s.chars()
            .map(|c| match c {
                '\u{200d}' | '\u{1f1e6}'..='\u{1f1ff}' => 1,
                '\u{1f3fb}'..='\u{1f3ff}' => 2,
                c => c.width().unwrap_or(0),
            })
            .sum()
    }
    pub fn width(self, s: &str) -> usize {
        s.graphemes(true).map(|g| self.cluster(g)).sum()
    }
    pub fn cut(self, s: &str, left: usize, right: usize) -> String {
        let mut x = 0;
        s.graphemes(true)
            .filter(|g| {
                let start = x;
                x += self.cluster(g);
                start >= left && x <= right
            })
            .collect()
    }
    pub fn truncate(self, s: &str, width: usize, tail: &str) -> String {
        if self.width(s) <= width {
            return s.into();
        }
        let n = self.width(tail);
        if n > width {
            return String::new();
        }
        self.cut(s, 0, width - n) + tail
    }
    pub fn snap(self, s: &str, col: usize, end: bool) -> usize {
        let mut x = 0;
        for g in s.graphemes(true) {
            let next = x + self.cluster(g);
            if col < next {
                return if end { next } else { x };
            }
            x = next;
        }
        x
    }
    pub fn draw(self, buf: &mut Buffer, x: u16, y: u16, width: u16, s: &str, style: Style) {
        let mut col = 0;
        for g in s.graphemes(true) {
            let n = self.cluster(g);
            if col + n > usize::from(width) {
                break;
            }
            if n == 0 {
                continue;
            }
            if let Some(cell) = buf.cell_mut((x + col as u16, y)) {
                cell.set_symbol(g).set_style(style);
                // Terminal.app uses different emoji widths than modern terminals.
                cell.set_diff_option(CellDiffOption::ForcedWidth(
                    NonZeroU16::new(n as u16).unwrap(),
                ));
            }
            col += n;
        }
    }
}
