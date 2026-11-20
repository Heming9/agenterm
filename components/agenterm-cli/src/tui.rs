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
    ui::{self, AuthorizationPrompt, ConnectionMode, Event as ClientEvent, Options, Reporter, tr},
};
use clipboard::Clipboard;
use crossterm::{
    cursor,
    event::{
        self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers, MouseButton, MouseEvent,
        MouseEventKind,
    },
    execute,
    style::Stylize,
    terminal::{self, EnterAlternateScreen, LeaveAlternateScreen},
};
use input::Input;
use log::{Log, LogOptions, Logger, StreamOptions, clean};
use ratatui::{
    Terminal,
    backend::{CrosstermBackend, IntoCrossterm},
    style::Color,
};
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

mod colors {
    include!(concat!(env!("OUT_DIR"), "/theme_colors.rs"));
}

const CONNECTION_LOG: Logger = Logger::new(LogOptions {
    timestamp: true,
    ..LogOptions::DEFAULT
});

const COMMAND_LOG: Logger = Logger::new(LogOptions {
    color: Some(colors::PRIMARY),
    timestamp: true,
    prefix: "$ ",
    foldable: true,
    spacing: 2,
    ..LogOptions::DEFAULT
});

const RETRY_LOG: Logger = Logger::new(LogOptions {
    color: Some(colors::ERROR),
    timestamp: true,
    prefix_color: Some(colors::ERROR),
    foldable: true,
    spacing: 2,
    merge: true,
    ..LogOptions::DEFAULT
});

const SYSTEM_LOG: Logger = Logger::new(LogOptions {
    color: Some(colors::SECONDARY),
    spacing: 2,
    ..LogOptions::DEFAULT
});

const COMMAND_OUTPUT: StreamOptions = StreamOptions {
    color: None,
    foldable: true,
    spacing: 2,
};

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

pub fn consent(
    prompt: &AuthorizationPrompt<'_>,
    mode: Option<ConnectionMode>,
) -> Result<Option<ConnectionMode>> {
    let color = view::Palette::current().color;
    if color {
        println!(
            "{}",
            prompt.formatted(
                prompt
                    .title
                    .with(Color::from_u32(colors::WARNING).into_crossterm()),
                prompt
                    .server
                    .with(Color::from_u32(colors::PRIMARY).into_crossterm()),
            ),
        );
    } else {
        println!("{}", prompt.formatted(prompt.title, prompt.server));
    }
    let _guard = TerminalGuard::enter(false).map_err(|e| e.to_string())?;
    let mut out = io::stdout();
    let mut selected = 0usize;
    let choices = ui::consent_choices(mode);
    let mut lines = 0;
    let metrics = Metrics {
        legacy: std::env::var("TERM_PROGRAM").unwrap_or_default() == "Apple_Terminal",
    };
    loop {
        if lines > 1 {
            execute!(out, cursor::MoveUp(lines - 1)).map_err(|e| e.to_string())?;
        }
        let columns = match terminal::size() {
            Ok((width, _)) if width > 0 => usize::from(width),
            _ => 80,
        };
        let rows: Vec<_> = choices
            .iter()
            .enumerate()
            .map(|(index, choice)| ui::consent_row(*choice, index == selected))
            .collect();
        lines = rows
            .iter()
            .map(|row| metrics.width(row).div_ceil(columns) as u16)
            .sum();
        write!(out, "\r\x1b[J").map_err(|e| e.to_string())?;
        for (index, row) in rows.into_iter().enumerate() {
            if index != 0 {
                write!(out, "\r\n").map_err(|e| e.to_string())?;
            }
            if color && index == selected {
                write!(out, "\x1b[7m{row}\x1b[0m").map_err(|e| e.to_string())?;
            } else {
                write!(out, "{row}").map_err(|e| e.to_string())?;
            }
        }
        out.flush().map_err(|e| e.to_string())?;
        match event::read().map_err(|e| e.to_string())? {
            Event::Key(k) if k.kind != KeyEventKind::Release => match k.code {
                KeyCode::Up | KeyCode::Left => selected = selected.saturating_sub(1),
                KeyCode::Down | KeyCode::Right => selected = (selected + 1).min(choices.len() - 1),
                KeyCode::Enter => {
                    write!(out, "\r\n").map_err(|e| e.to_string())?;
                    return Ok(choices[selected]);
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
    requested_mode: Option<ConnectionMode>,
    allowed_mode: Option<ConnectionMode>,
    mode: ConnectionMode,
    palette: view::Palette,
    state: State,
    menu: Menu,
    servers: Vec<String>,
    address: String,
    identity: String,
    message: String,
    ended_message: Option<String>,
    latency: Option<u64>,
    ever_connected: bool,
    show_welcome: bool,
    log: Log,
    completed_commands: usize,
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
    register_link: bool,
    exit_result: Option<Result<()>>,
}
impl App {
    fn new(options: Options) -> Self {
        let mut identity = format!(
            "{}@{}",
            crate::terminal::username(),
            crate::terminal::hostname()
        );
        identity.retain(|c| !c.is_control());
        Self {
            input: Input::new(options.token),
            requested_mode: options.mode,
            allowed_mode: options.mode,
            mode: ConnectionMode::default(),
            palette: view::Palette::current(),
            state: State::Disconnected,
            menu: Menu::Input,
            servers: vec![],
            address: String::new(),
            identity,
            message: String::new(),
            ended_message: None,
            latency: None,
            ever_connected: false,
            show_welcome: true,
            log: Log::default(),
            completed_commands: 0,
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
            register_link: false,
            exit_result: None,
        }
    }
    fn body_height(&self) -> usize {
        usize::from(self.height.saturating_sub(5).max(1))
    }
    fn body_width(&self) -> usize {
        usize::from(self.width.saturating_sub(2).max(1))
    }
    fn log_height(&self) -> usize {
        if self.state == State::Disconnected {
            self.log.welcome_rows
        } else {
            self.body_height()
        }
    }
    fn max_scroll(&self) -> usize {
        self.log.rows.len().saturating_sub(self.body_height())
    }
    fn refresh(&mut self) {
        let height = self.body_height();
        let welcome = match self.state {
            State::Disconnected if !matches!(self.menu, Menu::Mode(_)) => {
                let space = height.saturating_sub(self.connection_form().height());
                (space > 0).then_some(space)
            }
            State::Connecting if self.show_welcome && !self.ever_connected => Some(height),
            State::Connected if self.show_welcome => Some(height),
            _ => None,
        };
        if self.log.evicted || self.log.welcome_height != welcome {
            self.selection = None;
        }
        self.log.refresh(
            self.body_width(),
            self.metrics,
            self.selection.is_some(),
            welcome,
        );
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
    fn begin(&mut self) {
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
            self.message.clear();
            return;
        }
        if connection::target_address(value).is_err() {
            self.message = tr("invalid.agenterm.connection.address.or.token").into();
            return;
        }
        match connection::authorization(value, self.requested_mode) {
            Ok((_, mode)) => {
                self.allowed_mode = mode;
                self.message.clear();
                self.menu = Menu::Mode(0);
            }
            Err(error) => self.message = error,
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
        self.input.set(value.clone());
        self.address = clean(&address);
        self.state = State::Connecting;
        self.menu = Menu::Input;
        self.message.clear();
        self.ended_message = None;
        self.latency = None;
        self.ever_connected = false;
        self.show_welcome = true;
        self.log = Log::default();
        self.completed_commands = 0;
        self.scroll = 0;
        self.follow = true;
        self.selection = None;
        let stopped = Arc::new(AtomicBool::new(false));
        let stop = stopped.clone();
        let (tx, events) = mpsc::sync_channel(256);
        let reporter = Reporter::channel(tx, stop.clone());
        let mode = self.mode;
        let register_link = self.register_link;
        let thread = std::thread::spawn(move || {
            connection::run(&value, mode, stop, reporter, register_link)
        });
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
            ClientEvent::Redeemed {
                token,
                server_version,
                elapsed,
            } => {
                self.address = clean(&token.connection_url());
                self.input.set(token.encoded);
                let release = server_version.map_or_else(
                    || tr("tui.connection.unknown_version").into(),
                    |version| format!("v{}", version.strip_prefix('v').unwrap_or(&version)),
                );
                CONNECTION_LOG.append(
                    &mut self.log,
                    &format!(
                        "{} · {} {release} · {} {} ms",
                        tr("tui.connection.token_validated"),
                        tr("tui.connection.server_version"),
                        tr("tui.connection.elapsed"),
                        elapsed.as_millis()
                    ),
                );
            }
            ClientEvent::Connecting(address) => {
                if self.state != State::Disconnecting {
                    self.state = State::Connecting;
                    self.address = clean(&address);
                    self.latency = None;
                    CONNECTION_LOG.append(
                        &mut self.log,
                        &format!("{} {}", tr("tui.connection.connecting"), self.address),
                    );
                }
            }
            ClientEvent::WebSocketUpgraded(status) => {
                if self.state != State::Disconnecting {
                    CONNECTION_LOG.append(
                        &mut self.log,
                        &format!("{} · HTTP {status}", tr("tui.connection.handshake")),
                    );
                }
            }
            ClientEvent::Connected { elapsed } => {
                if self.state == State::Disconnecting {
                    return;
                }
                self.state = State::Connected;
                self.latency = None;
                self.show_welcome = !self.ever_connected;
                let time = ui::reporter_timestamp();
                if self.ever_connected {
                    CONNECTION_LOG.append_at(&mut self.log, &time, tr("connection.restored"));
                } else {
                    CONNECTION_LOG.append_at(
                        &mut self.log,
                        &time,
                        &format!(
                            "{} · {} {} ms",
                            tr("tui.connection.connected"),
                            tr("tui.connection.total_elapsed"),
                            elapsed.as_millis()
                        ),
                    );
                    CONNECTION_LOG.append_at(&mut self.log, &time, tr("tui.welcome.waiting"));
                }
                self.ever_connected = true;
            }
            ClientEvent::Retry { message, delay } => {
                if self.state != State::Disconnecting {
                    self.state = State::Reconnecting(delay);
                    self.latency = None;
                }
                RETRY_LOG.append(&mut self.log, &message);
            }
            ClientEvent::Latency(ms) => {
                if self.state == State::Connected && ms <= 60_000 {
                    self.latency = Some(ms);
                }
            }
            ClientEvent::Message(message) => {
                self.ended_message = Some(clean(&message));
                SYSTEM_LOG.append(&mut self.log, &message);
            }
            ClientEvent::Started { id, command, time } => {
                self.show_welcome = false;
                COMMAND_LOG.append_at(&mut self.log, &time, &command);
                self.log.open_stream(id, COMMAND_OUTPUT);
            }
            ClientEvent::Output { id, data } => self.log.append_stream(&id, &data),
            ClientEvent::Finished { id, .. } => {
                if self.log.close_stream(&id) {
                    self.completed_commands += 1;
                }
            }
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
            if self.mode == ConnectionMode::Temporary {
                self.exiting = true;
                self.exit_result = Some(result);
                return true;
            }
            self.state = State::Disconnected;
            self.latency = None;
            self.selection = None;
            self.log = Log::default();
            self.completed_commands = 0;
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
                    KeyCode::Up | KeyCode::Left => selected = selected.saturating_sub(1),
                    KeyCode::Down | KeyCode::Right => {
                        selected =
                            (selected + 1).min(ui::consent_choices(self.allowed_mode).len() - 1)
                    }
                    KeyCode::Esc => {
                        self.menu = Menu::Input;
                        return;
                    }
                    KeyCode::Enter => {
                        let mode = ui::consent_choices(self.allowed_mode)[selected];
                        let visible =
                            mode.is_none() || self.connection_form().height() <= self.body_height();
                        self.menu = Menu::Input;
                        if !visible {
                            self.message = ui::text(
                                "Enlarge the terminal to review authorization.",
                                "请放大终端窗口后查看并确认授权。",
                            )
                            .into();
                        } else if let Some(mode) = mode {
                            self.mode = mode;
                            self.connect();
                        }
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
                        self.begin();
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
                    self.message.clear();
                }
            } else {
                self.disconnect();
            }
            return;
        }
        if self.state == State::Disconnected {
            if key.code == KeyCode::Enter {
                self.begin();
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
        x < self.width && y >= 2 && usize::from(y) < 2 + self.log_height()
    }
    fn point(&self, x: u16, y: u16) -> Point {
        Point {
            row: (self.scroll
                + usize::from(y.saturating_sub(2)).min(self.log_height().saturating_sub(1)))
            .min(self.log.rows.len().saturating_sub(1)),
            col: usize::from(x.min(self.width.saturating_sub(1))),
        }
    }
    fn mouse(&mut self, mouse: MouseEvent) {
        if self.log_height() == 0 {
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

pub fn run(
    options: Options,
    shutdown: Arc<AtomicBool>,
    authorized: Option<ConnectionMode>,
    register_link: bool,
) -> Result<()> {
    let guard = TerminalGuard::enter(true).map_err(|e| e.to_string())?;
    let mut terminal =
        Terminal::new(CrosstermBackend::new(io::stdout())).map_err(|e| e.to_string())?;
    let mut app = App::new(options);
    app.register_link = register_link;
    if let Ok((w, h)) = terminal::size() {
        app.resize(w, h);
    }
    if let Some(mode) = authorized {
        app.mode = mode;
        app.connect();
    } else if !app.input.value.is_empty() {
        app.begin();
    }
    let result = (|| -> io::Result<()> {
        app.refresh();
        terminal.draw(|frame| app.render(frame))?;
        let mut dirty = false;
        let mut tick = Instant::now();
        loop {
            if shutdown.load(Ordering::SeqCst) {
                app.exiting = true;
                app.disconnect();
            }
            dirty |= app.drain();
            if dirty {
                app.refresh();
            }
            if tick.elapsed() >= Duration::from_millis(50) {
                dirty |= app.selection_tick();
                tick = Instant::now();
            }
            if app.exiting && app.worker.is_none() {
                break;
            }
            if dirty {
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
    result.map_err(|e| e.to_string())?;
    app.exit_result.unwrap_or(Ok(()))
}
