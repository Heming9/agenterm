use crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use unicode_segmentation::UnicodeSegmentation;

#[derive(Default)]
pub struct Input {
    pub value: String,
    pub cursor: usize,
}
impl Input {
    pub fn new(value: String) -> Self {
        let cursor = value.len();
        Self { value, cursor }
    }
    pub fn set(&mut self, value: String) {
        self.cursor = value.len();
        self.value = value;
    }
    pub fn insert(&mut self, s: &str) {
        let available = 4096usize.saturating_sub(self.value.chars().count());
        let s: String = s
            .chars()
            .filter(|c| !c.is_control())
            .take(available)
            .collect();
        self.value.insert_str(self.cursor, &s);
        self.cursor += s.len();
    }
    fn previous(&self) -> usize {
        self.value[..self.cursor]
            .grapheme_indices(true)
            .next_back()
            .map_or(0, |(i, _)| i)
    }
    fn next(&self) -> usize {
        self.value[self.cursor..]
            .graphemes(true)
            .next()
            .map_or(self.value.len(), |g| self.cursor + g.len())
    }
    pub fn key(&mut self, key: KeyEvent) {
        if key.modifiers.contains(KeyModifiers::CONTROL) {
            match key.code {
                KeyCode::Char('a') => self.cursor = 0,
                KeyCode::Char('e') => self.cursor = self.value.len(),
                KeyCode::Char('u') => {
                    self.value.drain(..self.cursor);
                    self.cursor = 0;
                }
                KeyCode::Char('k') => {
                    self.value.truncate(self.cursor);
                }
                KeyCode::Char('w') => {
                    let prefix = &self.value[..self.cursor];
                    let at = prefix
                        .trim_end()
                        .char_indices()
                        .rev()
                        .find(|(_, c)| c.is_whitespace())
                        .map_or(0, |(i, c)| i + c.len_utf8());
                    self.value.drain(at..self.cursor);
                    self.cursor = at;
                }
                _ => {}
            }
            return;
        }
        match key.code {
            KeyCode::Left => self.cursor = self.previous(),
            KeyCode::Right => self.cursor = self.next(),
            KeyCode::Home => self.cursor = 0,
            KeyCode::End => self.cursor = self.value.len(),
            KeyCode::Backspace => {
                let at = self.previous();
                self.value.drain(at..self.cursor);
                self.cursor = at;
            }
            KeyCode::Delete => {
                let end = self.next();
                self.value.drain(self.cursor..end);
            }
            KeyCode::Char(c) if !key.modifiers.contains(KeyModifiers::ALT) => {
                self.insert(&c.to_string())
            }
            _ => {}
        }
    }
}
