mod clipboard;
mod input;
mod log;
#[cfg(test)]
mod tests;
mod text;
mod view;

use crate::{
    connection,
    protocol::{self, Result},
    storage,
    ui::{self, Event as ClientEvent, Options, Reporter, tr},
};
use clipboard::Clipboard;
use crossterm::{
    cursor,
    event::{
        self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers, MouseButton, MouseEvent,
        MouseEventKind,
    },
    execute,
    terminal::{self, EnterAlternateScreen, LeaveAlternateScreen},
};
use input::Input;
use log::{Kind, Log, clean};
use ratatui::{Terminal, backend::CrosstermBackend};
use std::{
    io::{self, Write},
    sync::{
        Arc, Once,
        atomic::{AtomicBool, AtomicU8, Ordering},
        mpsc::{self, Receiver},
    },
    thread::JoinHandle,
    time::{Duration, Instant},
};
use text::Metrics;

static TERMINAL_MODE: AtomicU8 = AtomicU8::new(0);
struct TerminalGuard;
impl TerminalGuard {
    fn enter(screen: bool) -> io::Result<Self> {
        static HOOK: Once = Once::new();
        HOOK.call_once(|| {
            let previous = std::panic::take_hook();
            std::panic::set_hook(Box::new(move |info| {
                restore();
                previous(info);
            }));
        });
        terminal::enable_raw_mode()?;
        TERMINAL_MODE.store(if screen { 2 } else { 1 }, Ordering::SeqCst);
        let guard = Self;
        if screen {
            execute!(
                io::stdout(),
                EnterAlternateScreen,
                event::EnableMouseCapture,
                event::EnableBracketedPaste,
                cursor::Hide
            )?;
        }
        Ok(guard)
    }
}
fn restore() {
    let mode = TERMINAL_MODE.swap(0, Ordering::SeqCst);
    if mode == 2 {
        let _ = execute!(
            io::stdout(),
            event::DisableBracketedPaste,
            event::DisableMouseCapture,
            LeaveAlternateScreen,
            cursor::Show
        );
    }
    if mode > 0 {
        let _ = terminal::disable_raw_mode();
    }
}
impl Drop for TerminalGuard {
    fn drop(&mut self) {
        restore();
    }
}

// Go displays authorization in the main terminal before opening its alternate
// screen. Match its 120-column horizontal/vertical menu and safe default.
pub fn consent() -> Result<Option<bool>> {
    println!("\n{}", tr("installer.consent.menu"));
    let _guard = TerminalGuard::enter(false).map_err(|e| e.to_string())?;
    let mut out = io::stdout();
    let mut selected = 0;
    let mut lines = 0;
    loop {
        if lines > 1 {
            execute!(out, cursor::MoveUp(lines - 1)).map_err(|e| e.to_string())?;
        }
        let width = terminal::size().map_or(80, |s| s.0);
        let separator = if width < 120 { "\r\n" } else { "    " };
        let color = std::env::var_os("NO_COLOR").is_none()
            && std::env::var("TERM").unwrap_or_default() != "dumb";
        let choices = [
            tr("installer.consent.temporary"),
            tr("installer.consent.persistent"),
            tr("installer.consent.no"),
        ];
        let choices: Vec<String> = choices
            .iter()
            .enumerate()
            .map(|(i, s)| {
                if i == selected {
                    if color {
                        format!("\x1b[7m ▶ {s} \x1b[0m")
                    } else {
                        format!(" ▶ {s} ")
                    }
                } else {
                    format!("   {s} ")
                }
            })
            .collect();
        write!(out, "\r\x1b[J{}", choices.join(separator))
            .and_then(|_| out.flush())
            .map_err(|e| e.to_string())?;
        lines = if width < 120 { 3 } else { 1 };
        match event::read().map_err(|e| e.to_string())? {
            Event::Key(k) if k.kind != KeyEventKind::Release => match k.code {
                KeyCode::Up | KeyCode::Left => selected = selected.saturating_sub(1),
                KeyCode::Down | KeyCode::Right => selected = (selected + 1).min(2),
                KeyCode::Enter => {
                    write!(out, "\r\n").map_err(|e| e.to_string())?;
                    return Ok(match selected {
                        0 => Some(true),
                        1 => Some(false),
                        _ => None,
                    });
                }
                KeyCode::Esc => return Ok(None),
                KeyCode::Char('c') if k.modifiers.contains(KeyModifiers::CONTROL) => {
                    return Ok(None);
                }
                _ => {}
            },
            _ => {}
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Disconnected,
    Connecting,
    Connected,
    Reconnecting(u64),
    Disconnecting,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Menu {
    Input,
    Mode(usize),
    Server(usize),
}
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
struct Point {
    row: usize,
    col: usize,
}
struct Selection {
    anchor: Point,
    cursor: Point,
    down: bool,
    dragged: bool,
    mouse_x: u16,
    mouse_y: u16,
    id: u64,
    request: u64,
    notice: &'static str,
}
struct Worker {
    stopped: Arc<AtomicBool>,
    events: Receiver<ClientEvent>,
    thread: JoinHandle<Result<()>>,
}
struct App {
    input: Input,
    invitation: String,
    temporary: bool,
    mode_provided: bool,
    progress: bool,
    palette: view::Palette,
    state: State,
    menu: Menu,
    servers: Vec<String>,
    select_mode_after_server: bool,
    address: String,
    message: String,
    ended_message: Option<String>,
    latency: Option<u64>,
    ever_connected: bool,
    log: Log,
    scroll: usize,
    follow: bool,
    width: u16,
    height: u16,
    metrics: Metrics,
    selection: Option<Selection>,
    selection_id: u64,
    clipboard: Clipboard,
    worker: Option<Worker>,
    exiting: bool,
}
impl App {
    fn new(options: Options) -> Self {
        let value = if options.invite.is_empty() {
            options.token
        } else {
            options.invite.clone()
        };
        Self {
            input: Input::new(value),
            invitation: options.invite.clone(),
            temporary: options.temporary,
            mode_provided: options.mode_provided,
            progress: !options.invite.is_empty(),
            palette: view::Palette::current(),
            state: State::Disconnected,
            menu: Menu::Input,
            servers: vec![],
            select_mode_after_server: false,
            address: String::new(),
            message: String::new(),
            ended_message: None,
            latency: None,
            ever_connected: false,
            log: Log::default(),
            scroll: 0,
            follow: true,
            width: 80,
            height: 24,
            metrics: Metrics {
                legacy: std::env::var("TERM_PROGRAM").unwrap_or_default() == "Apple_Terminal",
            },
            selection: None,
            selection_id: 0,
            clipboard: Clipboard::default(),
            worker: None,
            exiting: false,
        }
    }
    fn body_height(&self) -> usize {
        usize::from(self.height.saturating_sub(5).max(1))
    }
    fn body_width(&self) -> usize {
        usize::from(self.width.saturating_sub(2).max(1))
    }
    fn max_scroll(&self) -> usize {
        self.log.rows.len().saturating_sub(self.body_height())
    }
    fn refresh(&mut self) {
        if self.log.evicted {
            self.selection = None;
        }
        self.log
            .refresh(self.body_width(), self.metrics, self.selection.is_some());
        if self.follow {
            self.scroll = self.max_scroll();
        } else {
            self.scroll = self.scroll.min(self.max_scroll());
        }
    }
    fn resize(&mut self, width: u16, height: u16) {
        self.selection = None;
        self.width = width;
        self.height = height;
        self.refresh();
    }
    fn begin(&mut self, choose_mode: bool) {
        let value = self.input.value.trim();
        if protocol::short_code(value) {
            self.servers = match storage::history() {
                Ok(servers) if !servers.is_empty() => servers,
                Ok(_) => {
                    self.message = tr("history.server.empty").into();
                    return;
                }
                Err(_) => {
                    self.message = tr("history.server.failed").into();
                    return;
                }
            };
            self.menu = Menu::Server(0);
            self.select_mode_after_server = choose_mode;
            self.message.clear();
            return;
        }
        if connection::display_address(value).is_err() {
            self.message = tr("invalid.agenterm.connection.address.or.token").into();
            return;
        }
        self.message.clear();
        if choose_mode {
            self.menu = Menu::Mode(0);
        } else {
            self.connect();
        }
    }
    fn connect(&mut self) {
        if self.worker.is_some() {
            return;
        }
        let value = self.input.value.trim().to_owned();
        let address = match connection::display_address(&value) {
            Ok(a) => a,
            Err(e) => {
                self.message = e;
                return;
            }
        };
        let invited = !self.invitation.is_empty() && value == self.invitation;
        self.input.set(value.clone());
        self.address = clean(&address);
        self.state = State::Connecting;
        self.menu = Menu::Input;
        self.message.clear();
        self.ended_message = None;
        self.latency = None;
        self.ever_connected = false;
        self.log = Log::default();
        self.scroll = 0;
        self.follow = true;
        self.selection = None;
        let stopped = Arc::new(AtomicBool::new(false));
        let stop = stopped.clone();
        let (tx, events) = mpsc::sync_channel(256);
        let reporter = Reporter::channel(tx, stop.clone());
        let temporary = self.temporary;
        let thread =
            std::thread::spawn(move || connection::run(&value, invited, temporary, stop, reporter));
        self.worker = Some(Worker {
            stopped,
            events,
            thread,
        });
    }
    fn disconnect(&mut self) {
        if let Some(worker) = &self.worker {
            worker.stopped.store(true, Ordering::SeqCst);
            self.state = State::Disconnecting;
            self.latency = None;
        }
    }
    fn client_event(&mut self, event: ClientEvent) {
        match event {
            ClientEvent::Redeemed(token) => {
                self.address = clean(&token.connection_url());
                self.input.set(token.encoded);
                self.invitation.clear();
            }
            ClientEvent::Connecting => {
                if self.state != State::Disconnecting {
                    self.state = State::Connecting;
                    self.latency = None;
                }
            }
            ClientEvent::Connected => {
                if self.state == State::Disconnecting {
                    return;
                }
                self.state = State::Connected;
                self.latency = None;
                if self.ever_connected {
                    self.log.message(
                        Kind::System,
                        &format!("{} {}", ui::reporter_timestamp(), tr("connection.restored")),
                    );
                } else if self.progress {
                    self.log.message(Kind::System, tr("4.4.connected"));
                }
                self.ever_connected = true;
            }
            ClientEvent::Retry { message, delay } => {
                if self.state != State::Disconnecting {
                    self.state = State::Reconnecting(delay);
                    self.latency = None;
                }
                self.log.message(
                    Kind::Error,
                    &format!("{} {message}", ui::reporter_timestamp()),
                );
            }
            ClientEvent::Latency(ms) => {
                if self.state == State::Connected && ms <= 60_000 {
                    self.latency = Some(ms);
                }
            }
            ClientEvent::Message(message) => {
                self.ended_message = Some(clean(&message));
                self.log.message(Kind::System, &message);
            }
            event => self.log.event(event),
        }
    }
    fn drain(&mut self) -> bool {
        let mut changed = false;
        for _ in 0..256 {
            let event = self.worker.as_ref().and_then(|w| w.events.try_recv().ok());
            let Some(event) = event else {
                break;
            };
            self.client_event(event);
            changed = true;
        }
        // Join only after consuming every queued lifecycle/output event.
        if self.worker.as_ref().is_some_and(|w| w.thread.is_finished()) {
            while let Some(event) = self.worker.as_ref().and_then(|w| w.events.try_recv().ok()) {
                self.client_event(event);
            }
            let worker = self.worker.take().unwrap();
            let result = worker
                .thread
                .join()
                .unwrap_or_else(|_| Err("connection worker failed".into()));
            self.state = State::Disconnected;
            self.latency = None;
            self.selection = None;
            self.menu = Menu::Input;
            self.message = self.ended_message.take().unwrap_or_else(|| match result {
                Ok(()) => tr("disconnected.2").into(),
                Err(e) => clean(&e),
            });
            changed = true;
        }
        while let Ok(copied) = self.clipboard.rx.try_recv() {
            if let Some(sequence) = self.copy_result(copied) {
                let _ = io::stdout()
                    .write_all(sequence.as_bytes())
                    .and_then(|_| io::stdout().flush());
            }
            changed = true;
        }
        if changed {
            self.refresh();
        }
        changed
    }
    fn copy_result(&mut self, copied: clipboard::Copied) -> Option<String> {
        // Completing a requested copy must not depend on its selection still
        // being visible. Only obsolete requests are discarded; notices belong
        // exclusively to the selection that initiated the request.
        if !self.clipboard.is_latest(copied.request) {
            return None;
        }
        if let Some(selection) = self.selection.as_mut()
            && selection.id == copied.selection
            && selection.request == copied.request
        {
            selection.notice = copied.notice;
        }
        copied.sequence
    }
    fn clear_selection(&mut self) {
        let scroll = self.scroll;
        self.selection = None;
        self.follow = false;
        self.refresh();
        self.scroll = scroll.min(self.max_scroll());
        self.follow = self.scroll == self.max_scroll();
    }
    fn scroll_by(&mut self, n: isize) {
        self.scroll = self.scroll.saturating_add_signed(n).min(self.max_scroll());
        self.follow = self.scroll == self.max_scroll();
    }
    fn key(&mut self, key: KeyEvent) {
        if key.kind == KeyEventKind::Release {
            return;
        }
        let ctrl_c = matches!(key.code, KeyCode::Char('c' | 'C'))
            && key.modifiers.contains(KeyModifiers::CONTROL);
        match self.menu {
            Menu::Mode(mut selected) => {
                match key.code {
                    KeyCode::Up | KeyCode::Left => selected = 0,
                    KeyCode::Down | KeyCode::Right => selected = 1,
                    KeyCode::Esc => {
                        self.menu = Menu::Input;
                        return;
                    }
                    KeyCode::Enter => {
                        self.temporary = selected == 0;
                        self.connect();
                        return;
                    }
                    _ if ctrl_c => {
                        self.menu = Menu::Input;
                        return;
                    }
                    _ => {}
                }
                self.menu = Menu::Mode(selected);
                return;
            }
            Menu::Server(mut selected) => {
                match key.code {
                    KeyCode::Up | KeyCode::Char('k') => selected = selected.saturating_sub(1),
                    KeyCode::Down | KeyCode::Char('j') => {
                        selected = (selected + 1).min(self.servers.len() - 1)
                    }
                    KeyCode::Esc => {
                        self.menu = Menu::Input;
                        return;
                    }
                    KeyCode::Enter => {
                        self.input.set(format!(
                            "{}/c/{}",
                            self.servers[selected],
                            self.input.value.trim().to_ascii_uppercase()
                        ));
                        self.menu = Menu::Input;
                        self.begin(self.select_mode_after_server);
                        return;
                    }
                    _ if ctrl_c => {
                        self.menu = Menu::Input;
                        return;
                    }
                    _ => {}
                }
                self.menu = Menu::Server(selected);
                return;
            }
            Menu::Input => {}
        }
        if self.selection.is_some() {
            if ctrl_c && self.selection.as_ref().is_some_and(|s| s.dragged) {
                self.copy();
                return;
            }
            if !matches!(
                key.code,
                KeyCode::Up
                    | KeyCode::Down
                    | KeyCode::PageUp
                    | KeyCode::PageDown
                    | KeyCode::Home
                    | KeyCode::End
            ) {
                self.clear_selection();
            }
            if key.code == KeyCode::Esc {
                return;
            }
        }
        if ctrl_c {
            if self.state == State::Disconnected {
                if self.input.value.is_empty() {
                    self.exiting = true;
                } else {
                    self.input.set(String::new());
                    self.invitation.clear();
                    self.message.clear();
                }
            } else {
                self.disconnect();
            }
            return;
        }
        if self.state == State::Disconnected {
            if key.code == KeyCode::Enter {
                self.begin(!self.mode_provided);
            } else {
                self.input.key(key);
            }
        } else {
            let page = self.body_height().saturating_sub(1).max(1) as isize;
            match key.code {
                KeyCode::Up => self.scroll_by(-1),
                KeyCode::Down => self.scroll_by(1),
                KeyCode::PageUp => self.scroll_by(-page),
                KeyCode::PageDown => self.scroll_by(page),
                KeyCode::Home => {
                    self.scroll = 0;
                    self.follow = false;
                }
                KeyCode::End => {
                    self.scroll = self.max_scroll();
                    self.follow = true;
                }
                _ => {}
            }
        }
    }
    fn inside(&self, x: u16, y: u16) -> bool {
        self.state != State::Disconnected
            && usize::from(x) < self.body_width()
            && y >= 2
            && usize::from(y) < 2 + self.body_height()
    }
    fn point(&self, x: u16, y: u16) -> Point {
        Point {
            row: (self.scroll + usize::from(y.saturating_sub(2)).min(self.body_height() - 1))
                .min(self.log.rows.len().saturating_sub(1)),
            col: usize::from(x).min(self.body_width() - 1),
        }
    }
    fn mouse(&mut self, mouse: MouseEvent) {
        if self.state == State::Disconnected {
            return;
        }
        match mouse.kind {
            MouseEventKind::ScrollUp | MouseEventKind::ScrollDown => {
                if self.inside(mouse.column, mouse.row) {
                    self.scroll_by(if mouse.kind == MouseEventKind::ScrollUp {
                        -3
                    } else {
                        3
                    });
                    if let Some(s) = &self.selection {
                        let point = self.point(s.mouse_x, s.mouse_y);
                        if let Some(s) = self.selection.as_mut()
                            && s.down
                        {
                            s.dragged = true;
                            s.cursor = point;
                        }
                    }
                }
            }
            MouseEventKind::Down(MouseButton::Left) => {
                if !self.inside(mouse.column, mouse.row) || self.log.rows.is_empty() {
                    self.clear_selection();
                    return;
                }
                self.selection_id += 1;
                let p = self.point(mouse.column, mouse.row);
                self.selection = Some(Selection {
                    anchor: p,
                    cursor: p,
                    down: true,
                    dragged: false,
                    mouse_x: mouse.column,
                    mouse_y: mouse.row,
                    id: self.selection_id,
                    request: 0,
                    notice: "",
                });
            }
            MouseEventKind::Drag(MouseButton::Left) => {
                let p = self.point(mouse.column, mouse.row);
                let inside = self.inside(mouse.column, mouse.row);
                if let Some(s) = self.selection.as_mut()
                    && s.down
                {
                    s.cursor = p;
                    s.mouse_x = mouse.column;
                    s.mouse_y = mouse.row;
                    s.dragged |= s.anchor != p || !inside;
                }
            }
            MouseEventKind::Up(MouseButton::Left) => {
                let p = self.point(mouse.column, mouse.row);
                let inside = self.inside(mouse.column, mouse.row);
                if let Some(s) = self.selection.as_mut()
                    && s.down
                {
                    s.cursor = p;
                    s.down = false;
                    s.dragged |= s.anchor != p;
                    if s.dragged {
                        self.copy();
                    } else {
                        self.selection = None;
                        if inside && let Some(start) = self.log.fold_start(p.row) {
                            self.log.toggle(p.row);
                            self.scroll = self.scroll.min(start);
                            self.follow = false;
                        }
                        self.refresh();
                        self.follow = self.scroll == self.max_scroll();
                    }
                }
            }
            _ => {}
        }
    }
    fn selection_tick(&mut self) -> bool {
        let Some(s) = &self.selection else {
            return false;
        };
        if !s.down || !s.dragged {
            return false;
        }
        let (x, y) = (s.mouse_x, s.mouse_y);
        let direction = if y <= 2 {
            -1
        } else if usize::from(y) > self.body_height() {
            1
        } else {
            0
        };
        let before = self.scroll;
        self.scroll_by(direction);
        if direction != 0 {
            let p = self.point(x, y);
            self.selection.as_mut().unwrap().cursor = p;
        }
        before != self.scroll
    }
    fn columns(&self, row: usize) -> Option<(usize, usize)> {
        let s = self.selection.as_ref()?;
        if !s.dragged {
            return None;
        }
        let (a, b) = if s.anchor <= s.cursor {
            (s.anchor, s.cursor)
        } else {
            (s.cursor, s.anchor)
        };
        if row < a.row || row > b.row {
            return None;
        }
        let text = &self.log.rows.get(row)?.text;
        let left = if row == a.row {
            self.metrics.snap(text, a.col, false)
        } else {
            0
        };
        let right = if row == b.row {
            self.metrics.snap(text, b.col, true)
        } else {
            self.metrics.width(text)
        };
        Some((left, right))
    }
    fn selected_text(&self) -> String {
        let Some(s) = &self.selection else {
            return String::new();
        };
        if !s.dragged {
            return String::new();
        }
        let start = s.anchor.row.min(s.cursor.row);
        let end = s.anchor.row.max(s.cursor.row);
        let mut text = String::new();
        for row in start..=end.min(self.log.rows.len().saturating_sub(1)) {
            if let Some((left, right)) = self.columns(row) {
                text.push_str(&self.metrics.cut(&self.log.rows[row].text, left, right));
                if row < end && !self.log.rows[row].soft {
                    text.push('\n');
                }
            }
        }
        text
    }
    fn copy(&mut self) {
        let text = self.selected_text();
        if text.is_empty() {
            return;
        }
        if let Some(s) = self.selection.as_mut() {
            s.request = self.clipboard.copy(text, s.id);
        }
    }
    fn shutdown(&mut self) {
        if let Some(worker) = self.worker.take() {
            worker.stopped.store(true, Ordering::SeqCst);
            let _ = worker.thread.join();
        }
    }
}

pub fn run(options: Options, shutdown: Arc<AtomicBool>) -> Result<()> {
    let auto_connect =
        !options.token.is_empty() || !options.invite.is_empty() || options.token_provided;
    let palette = view::Palette::detect();
    let guard = TerminalGuard::enter(true).map_err(|e| e.to_string())?;
    let mut terminal =
        Terminal::new(CrosstermBackend::new(io::stdout())).map_err(|e| e.to_string())?;
    let mut app = App::new(options);
    app.palette = palette;
    if let Ok((w, h)) = terminal::size() {
        app.resize(w, h);
    }
    if auto_connect {
        app.begin(false);
    }
    let result = (|| -> io::Result<()> {
        let mut dirty = true;
        let mut tick = Instant::now();
        loop {
            if shutdown.load(Ordering::SeqCst) {
                app.exiting = true;
                app.disconnect();
            }
            dirty |= app.drain();
            if tick.elapsed() >= Duration::from_millis(50) {
                dirty |= app.selection_tick();
                tick = Instant::now();
            }
            if app.exiting && app.worker.is_none() {
                break;
            }
            if dirty {
                app.refresh();
                terminal.draw(|frame| app.render(frame))?;
                dirty = false;
            }
            if event::poll(Duration::from_millis(16))? {
                match event::read()? {
                    Event::Key(key) => app.key(key),
                    Event::Mouse(mouse) => app.mouse(mouse),
                    Event::Resize(w, h) => app.resize(w, h),
                    Event::Paste(text)
                        if app.state == State::Disconnected && app.menu == Menu::Input =>
                    {
                        app.input.insert(&text)
                    }
                    _ => {}
                }
                dirty = true;
            }
        }
        Ok(())
    })();
    // Restore the user's terminal before waiting for OS DNS/TLS calls to return.
    drop(terminal);
    drop(guard);
    app.shutdown();
    result.map_err(|e| e.to_string())
}
