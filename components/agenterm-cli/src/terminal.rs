mod platform;
#[cfg(windows)]
pub(crate) use platform::Job;
pub(crate) use platform::username;

use crate::{protocol::Result, ui};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    collections::{BTreeMap, HashMap},
    fs,
    io::{Read, Write},
    path::PathBuf,
    process::Child,
    sync::{
        Arc, Condvar, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

const CAPACITY: usize = 1 << 20;

pub fn shell() -> String {
    platform::shell()
}
pub fn hostname() -> String {
    platform::hostname()
}
pub fn random(n: usize) -> Result<String> {
    let mut b = vec![0; n];
    getrandom::fill(&mut b).map_err(|e| e.to_string())?;
    Ok(b.iter().map(|b| format!("{b:02x}")).collect())
}

#[derive(Clone)]
pub struct Manager {
    inner: Arc<ManagerInner>,
}
struct ManagerInner {
    sessions: Mutex<BTreeMap<String, Arc<Terminal>>>,
    stopped: Arc<AtomicBool>,
    closed: AtomicBool,
    activity: Arc<Mutex<Instant>>,
    reporter: ui::Reporter,
}
struct Terminal {
    id: String,
    state: Mutex<State>,
    changed: Condvar,
    input: Mutex<Option<Box<dyn Write + Send>>>,
    child: Mutex<Option<Child>>,
    directory: PathBuf,
    marker: Vec<u8>,
    activity: Arc<Mutex<Instant>>,
    exited: AtomicBool,
    reporter: ui::Reporter,
    #[cfg(unix)]
    pty: std::fs::File,
    #[cfg(windows)]
    job: platform::Job,
}
struct State {
    ready: bool,
    submitted: bool,
    active: bool,
    closed: bool,
    stopping: bool,
    retained: bool,
    command: String,
    command_id: String,
    code: Option<i32>,
    shell_code: i32,
    output: Vec<u8>,
    offset: u64,
    started: Instant,
    ended: Option<Instant>,
    retention_deadline: Instant,
}
impl State {
    fn status(&self) -> &'static str {
        if self.closed {
            "closed"
        } else if !self.active && self.retained && !self.stopping {
            "idle"
        } else {
            "running"
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    method: String,
    params: Value,
}
#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Setup {
    #[serde(default)]
    session_id: String,
    #[serde(default)]
    cwd: String,
    #[serde(default)]
    env: HashMap<String, String>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Execute {
    command: String,
    #[serde(default)]
    session_id: String,
    setup: Option<Setup>,
    wait_ms: Option<u64>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReadRequest {
    session_id: String,
    #[serde(default)]
    command_id: String,
    #[serde(default)]
    cursor: u64,
    #[serde(default)]
    wait_ms: u64,
    max_bytes: Option<usize>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Control {
    session_id: String,
    action: String,
    input: Option<String>,
    wait_ms: Option<u64>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Empty {}

fn parse<T: serde::de::DeserializeOwned>(v: Value) -> Result<T> {
    serde_json::from_value(v).map_err(|e| e.to_string())
}
fn valid_id(s: &str) -> Result<()> {
    if !s.is_empty()
        && (s.trim().is_empty() || s.chars().count() > 64 || s.chars().any(char::is_control))
    {
        return Err("invalid session_id: use 1–64 printable characters".into());
    }
    Ok(())
}
fn wait_ms(n: u64) -> Result<Duration> {
    if n > 20000 {
        Err("wait_ms must be between 0 and 20000".into())
    } else {
        Ok(Duration::from_millis(n))
    }
}

impl Manager {
    #[cfg(test)]
    pub fn new(stopped: Arc<AtomicBool>) -> Self {
        Self::with_reporter(stopped, ui::Reporter::default())
    }
    pub fn reporter(&self) -> &ui::Reporter {
        &self.inner.reporter
    }
    pub fn with_reporter(stopped: Arc<AtomicBool>, reporter: ui::Reporter) -> Self {
        Self {
            inner: Arc::new(ManagerInner {
                sessions: Mutex::new(BTreeMap::new()),
                stopped,
                reporter,
                closed: AtomicBool::new(false),
                activity: Arc::new(Mutex::new(Instant::now())),
            }),
        }
    }
    pub fn touch(&self) {
        *self.inner.activity.lock().unwrap() = Instant::now();
    }
    pub fn idle_for(&self) -> Duration {
        if self
            .inner
            .sessions
            .lock()
            .unwrap()
            .values()
            .any(|t| t.state.lock().unwrap().active)
        {
            return Duration::ZERO;
        }
        self.inner.activity.lock().unwrap().elapsed()
    }
    fn canceled(&self, cancel: &AtomicBool) -> bool {
        cancel.load(Ordering::SeqCst)
            || self.inner.stopped.load(Ordering::SeqCst)
            || self.inner.closed.load(Ordering::SeqCst)
    }
    pub fn dispatch(&self, value: Value, cancel: &AtomicBool) -> Result<Value> {
        if self.canceled(cancel) {
            return Err("request canceled".into());
        }
        let r: Request = parse(value)?;
        match r.method.as_str() {
            "execute" => self.execute(parse(r.params)?, cancel),
            "read" => self.read(parse(r.params)?, cancel),
            "control" => self.control(parse(r.params)?, cancel),
            "list" => {
                let _: Empty = parse(r.params)?;
                self.list()
            }
            _ => Err(format!("unknown client API method: {}", r.method)),
        }
    }
    fn prune(sessions: &mut BTreeMap<String, Arc<Terminal>>) {
        let mut closed: Vec<_> = sessions
            .iter()
            .filter_map(|(id, t)| t.state.lock().unwrap().ended.map(|e| (id.clone(), e)))
            .collect();
        closed.sort_by_key(|(_, e)| *e);
        let count = closed.len();
        for (i, (id, e)) in closed.into_iter().enumerate() {
            if i < count.saturating_sub(10) || e.elapsed() > Duration::from_secs(1800) {
                sessions.remove(&id);
            }
        }
    }
    fn lookup(&self, id: &str) -> Result<Arc<Terminal>> {
        valid_id(id)?;
        let mut sessions = self.inner.sessions.lock().unwrap();
        Self::prune(&mut sessions);
        sessions.get(id).cloned().ok_or("unknown session".into())
    }
    fn execute(&self, a: Execute, cancel: &AtomicBool) -> Result<Value> {
        if a.command.trim().is_empty() {
            return Err("command is required".into());
        }
        valid_id(&a.session_id)?;
        let wait = wait_ms(a.wait_ms.unwrap_or(10000))?;
        let deadline = Instant::now() + wait;
        if let Some(setup) = &a.setup {
            valid_id(&setup.session_id)?;
            if !a.session_id.is_empty() {
                return Err("setup and session_id cannot be combined".into());
            }
            if setup.cwd.contains('\0')
                || setup
                    .env
                    .iter()
                    .any(|(k, v)| k.is_empty() || k.contains(['=', '\0']) || v.contains('\0'))
            {
                return Err("invalid cwd or environment".into());
            }
        }
        let mut sessions = self.inner.sessions.lock().unwrap();
        Self::prune(&mut sessions);
        // Close races cannot admit a terminal after shutdown's cleanup pass.
        if self.inner.closed.load(Ordering::SeqCst) {
            return Err("client is shutting down".into());
        }
        let mut id = a
            .setup
            .as_ref()
            .map(|s| s.session_id.clone())
            .unwrap_or_else(|| a.session_id.clone());
        let t = if let Some(t) = sessions
            .get(&id)
            .filter(|t| !t.state.lock().unwrap().closed)
        {
            if a.setup.is_some() {
                return Err("session already exists".into());
            }
            t.clone()
        } else {
            if !a.session_id.is_empty() {
                return Err("unknown session".into());
            }
            if sessions
                .values()
                .filter(|t| !t.state.lock().unwrap().closed)
                .count()
                >= 10
            {
                return Err("session limit reached; try again later".into());
            }
            if id.is_empty() {
                loop {
                    id = random(3)?;
                    if !sessions.contains_key(&id) {
                        break;
                    }
                }
            }
            let setup = a.setup.as_ref();
            let retained = setup.is_some_and(|s| !s.session_id.is_empty()) || wait.is_zero();
            let t = Terminal::spawn(
                id.clone(),
                setup,
                retained,
                deadline,
                self.inner.activity.clone(),
                self.inner.reporter.clone(),
            )?;
            sessions.insert(id.clone(), t.clone());
            t
        };
        let command_id = loop {
            let candidate = random(4)?;
            if sessions
                .values()
                .all(|s| s.state.lock().unwrap().command_id != candidate)
            {
                break candidate;
            }
        };
        let mut state = t.state.lock().unwrap();
        if state.active || state.stopping || state.closed {
            return Err("session is busy or closed".into());
        }
        state.active = true;
        state.submitted = false;
        state.command = a.command.clone();
        state.command_id = command_id;
        state.code = None;
        let command_id = state.command_id.clone();
        let cursor = state.offset + state.output.len() as u64;
        self.inner.reporter.started(&command_id, &a.command);
        drop(state);
        drop(sessions);
        self.touch();
        let worker = t.clone();
        std::thread::spawn(move || {
            if let Err(e) = worker.submit(&a.command) {
                worker.reporter.output(
                    &worker.state.lock().unwrap().command_id,
                    format!("[Agenterm] {e}\n").as_bytes(),
                );
                worker.stop();
            }
        });
        let mut state = t.state.lock().unwrap();
        loop {
            let now = Instant::now();
            if !state.retained && now >= state.retention_deadline && !state.closed {
                state.retained = true;
            }
            let finished = if state.retained {
                !state.active
            } else {
                state.closed
            };
            if finished || now >= deadline {
                break;
            }
            if self.canceled(cancel) {
                return Err("request canceled".into());
            }
            state = t
                .changed
                .wait_timeout(
                    state,
                    Duration::from_millis(20).min(deadline.saturating_duration_since(now)),
                )
                .unwrap()
                .0;
        }
        if !state.retained && Instant::now() >= deadline && !state.closed {
            state.retained = true;
        }
        let mut result = t.snapshot(&state, cursor, 16384, &command_id)?;
        if !state.retained
            && state.closed
            && result["has_more"] == false
            && result.get("truncated").is_none()
        {
            result.as_object_mut().unwrap().remove("session_id");
        }
        Ok(result)
    }
    fn read(&self, a: ReadRequest, cancel: &AtomicBool) -> Result<Value> {
        let wait = wait_ms(a.wait_ms)?;
        let limit = a.max_bytes.unwrap_or(16384);
        if !(4..=65536).contains(&limit) {
            return Err("max_bytes must be between 4 and 65536".into());
        }
        let t = self.lookup(&a.session_id)?;
        let deadline = Instant::now() + wait;
        let mut state = t.state.lock().unwrap();
        loop {
            if self.canceled(cancel) {
                return Err("request canceled".into());
            }
            if !a.command_id.is_empty() && a.command_id != state.command_id {
                return Err("session command changed".into());
            }
            if a.cursor > state.offset + state.output.len() as u64 {
                return Err("cursor is beyond available output".into());
            }
            if a.cursor < state.offset + state.output.len() as u64
                || !state.active
                || state.closed
                || Instant::now() >= deadline
            {
                return t.snapshot(&state, a.cursor, limit, &a.command_id);
            }
            state = t
                .changed
                .wait_timeout(state, Duration::from_millis(20))
                .unwrap()
                .0;
        }
    }
    fn list(&self) -> Result<Value> {
        let mut sessions = self.inner.sessions.lock().unwrap();
        Self::prune(&mut sessions);
        let mut items = vec![];
        for t in sessions.values() {
            let state = t.state.lock().unwrap();
            let command = if state.command.chars().count() > 160 {
                state.command.chars().take(157).collect::<String>() + "..."
            } else {
                state.command.clone()
            };
            let mut info = json!({"session_id":t.id,"command_id":state.command_id,"command":command,"status":state.status()});
            if let Some(code) = state.code {
                info["exit_code"] = json!(code);
            }
            items.push((state.started, info));
        }
        items.sort_by_key(|(time, _)| *time);
        Ok(json!({"sessions":items.into_iter().map(|(_,v)|v).collect::<Vec<_>>()}))
    }
    fn control(&self, a: Control, cancel: &AtomicBool) -> Result<Value> {
        if a.session_id.is_empty() {
            return Err("session_id is required".into());
        }
        let wait = wait_ms(a.wait_ms.unwrap_or(1000))?;
        if (a.action == "input" && (a.input.is_none() || a.wait_ms.is_some()))
            || (a.action != "input" && a.input.is_some())
            || (a.action != "stop" && a.wait_ms.is_some())
        {
            return Err("invalid control arguments".into());
        }
        let t = self.lookup(&a.session_id)?;
        match a.action.as_str() {
            "stop" => {
                self.touch();
                t.stop();
                let deadline = Instant::now() + wait;
                let mut state = t.state.lock().unwrap();
                while !state.closed && Instant::now() < deadline && !self.canceled(cancel) {
                    state = t
                        .changed
                        .wait_timeout(state, Duration::from_millis(20))
                        .unwrap()
                        .0;
                }
                Ok(if state.closed {
                    json!({"status":"closed","exit_code":state.shell_code})
                } else {
                    json!({"status":"running"})
                })
            }
            "input" | "interrupt" | "eof" => {
                {
                    let mut state = t.state.lock().unwrap();
                    while !state.submitted && !state.closed && !self.canceled(cancel) {
                        state = t
                            .changed
                            .wait_timeout(state, Duration::from_millis(20))
                            .unwrap()
                            .0;
                    }
                    if self.canceled(cancel) {
                        return Err("request canceled".into());
                    }
                    if state.closed {
                        return Err("session already ended".into());
                    }
                    if !state.active {
                        return Err("no command is running".into());
                    }
                }
                self.touch();
                if a.action == "input" {
                    t.write(a.input.unwrap().as_bytes())?;
                } else {
                    platform::control(&t, &a.action)?;
                }
                Ok(json!({"ok":true}))
            }
            _ => Err("unsupported session control".into()),
        }
    }
    pub fn close(&self) {
        self.inner.closed.store(true, Ordering::SeqCst);
        let sessions: Vec<_> = self
            .inner
            .sessions
            .lock()
            .unwrap()
            .values()
            .cloned()
            .collect();
        for t in &sessions {
            t.stop();
        }
        let deadline = Instant::now() + Duration::from_secs(3);
        while sessions.iter().any(|t| !t.state.lock().unwrap().closed) && Instant::now() < deadline
        {
            std::thread::sleep(Duration::from_millis(10));
        }
    }
}

impl Terminal {
    fn spawn(
        id: String,
        setup: Option<&Setup>,
        retained: bool,
        deadline: Instant,
        activity: Arc<Mutex<Instant>>,
        reporter: ui::Reporter,
    ) -> Result<Arc<Self>> {
        let directory = std::env::temp_dir().join(format!(
            "agenterm-rust-{}-{}",
            std::process::id(),
            random(12)?
        ));
        crate::storage::private_dir(&directory)?;
        let marker = format!("\x1eagenterm:{}:", random(24)?).into_bytes();
        let spawned = match platform::spawn(&marker, setup) {
            Ok(p) => p,
            Err(e) => {
                let _ = fs::remove_dir_all(&directory);
                return Err(e);
            }
        };
        let t = Arc::new(Self {
            id,
            state: Mutex::new(State {
                ready: false,
                submitted: false,
                active: false,
                closed: false,
                stopping: false,
                retained,
                command: String::new(),
                command_id: String::new(),
                code: None,
                shell_code: 0,
                output: vec![],
                offset: 0,
                started: Instant::now(),
                ended: None,
                retention_deadline: deadline,
            }),
            changed: Condvar::new(),
            input: Mutex::new(Some(spawned.input)),
            child: Mutex::new(Some(spawned.child)),
            directory,
            marker,
            activity,
            reporter,
            exited: AtomicBool::new(false),
            #[cfg(unix)]
            pty: spawned.pty,
            #[cfg(windows)]
            job: spawned.job,
        });
        let reader = t.clone();
        std::thread::spawn(move || reader.read_output(spawned.output));
        let waiter = t.clone();
        std::thread::spawn(move || {
            let code = loop {
                let result = waiter.child.lock().unwrap().as_mut().unwrap().try_wait();
                match result {
                    Ok(Some(s)) => break s.code().unwrap_or(-1),
                    Ok(None) => std::thread::sleep(Duration::from_millis(10)),
                    Err(_) => break -1,
                }
            };
            {
                let mut state = waiter.state.lock().unwrap();
                state.shell_code = code;
            }
            // A Windows descendant can keep the stdout pipe open after its
            // shell exits. End the owned Job so the reader can drain and close.
            #[cfg(windows)]
            waiter.stop();
            waiter.exited.store(true, Ordering::SeqCst);
            // A nonblocking PTY reader drains queued bytes before finalizing.
        });
        Ok(t)
    }
    fn submit(&self, command: &str) -> Result<()> {
        let mut state = self.state.lock().unwrap();
        let deadline = Instant::now() + Duration::from_secs(10);
        while !state.ready && !state.closed && Instant::now() < deadline {
            state = self
                .changed
                .wait_timeout(state, Duration::from_millis(20))
                .unwrap()
                .0;
        }
        if !state.ready || state.closed {
            return Err("shell did not become ready".into());
        }
        drop(state);
        let source = platform::command_script(command);
        let path = self.directory.join(format!(
            "{}.{}",
            random(8)?,
            if cfg!(windows) { "ps1" } else { "sh" }
        ));
        let mut file = crate::storage::open_private(&path)?;
        file.write_all(source.as_bytes())
            .map_err(|e| e.to_string())?;
        drop(file);
        self.write(platform::source_line(&path).as_bytes())?;
        self.state.lock().unwrap().submitted = true;
        self.changed.notify_all();
        Ok(())
    }
    fn write(&self, bytes: &[u8]) -> Result<()> {
        let mut input = self.input.lock().unwrap();
        let input = input.as_mut().ok_or("session input is closed")?;
        let mut rest = bytes;
        while !rest.is_empty() {
            match input.write(rest) {
                Ok(0) => return Err("session input is closed".into()),
                Ok(n) => rest = &rest[n..],
                Err(e)
                    if matches!(
                        e.kind(),
                        std::io::ErrorKind::WouldBlock | std::io::ErrorKind::Interrupted
                    ) =>
                {
                    if self.exited.load(Ordering::SeqCst) || self.state.lock().unwrap().stopping {
                        return Err("session is stopping".into());
                    }
                    std::thread::sleep(Duration::from_millis(5));
                }
                Err(e) => return Err(e.to_string()),
            }
        }
        input.flush().map_err(|e| e.to_string())
    }
    fn stop(&self) {
        {
            let mut s = self.state.lock().unwrap();
            if s.closed {
                return;
            }
            s.stopping = true;
        }
        let mut child = self.child.lock().unwrap();
        if let Some(child) = child.as_mut() {
            #[cfg(windows)]
            platform::stop(self, child);
            #[cfg(unix)]
            if matches!(child.try_wait(), Ok(None)) {
                platform::stop(self, child);
            }
        }
        self.changed.notify_all();
    }
    fn append(&self, bytes: &[u8]) {
        if bytes.is_empty() {
            return;
        }
        let mut state = self.state.lock().unwrap();
        state.output.extend_from_slice(bytes);
        if state.output.len() > CAPACITY {
            let mut n = state.output.len() - CAPACITY;
            while n < state.output.len() && (state.output[n] & 0xc0) == 0x80 {
                n += 1;
            }
            state.output.drain(..n);
            state.offset += n as u64;
        }
        self.changed.notify_all();
        self.reporter.output(&state.command_id, bytes);
        drop(state);
    }
    fn prompt(&self, code: i32) {
        let mut state = self.state.lock().unwrap();
        if !state.ready {
            state.ready = true;
            self.changed.notify_all();
            return;
        }
        if !state.active {
            return;
        }
        state.active = false;
        state.code = Some(code);
        if Instant::now() >= state.retention_deadline {
            state.retained = true;
        }
        let close = !state.retained;
        *self.activity.lock().unwrap() = Instant::now();
        self.changed.notify_all();
        self.reporter.finished(&state.command_id, code);
        drop(state);
        if close {
            self.stop();
        }
    }
    fn read_output(&self, mut output: Box<dyn Read + Send>) {
        let mut pending = vec![];
        let mut utf8 = vec![];
        let mut block = [0; 8192];
        loop {
            match output.read(&mut block) {
                Ok(0) => break,
                Ok(n) => {
                    utf8.extend_from_slice(&block[..n]);
                    // Preserve incomplete UTF-8 sequences across OS reads.
                    let (valid, left) = match std::str::from_utf8(&utf8) {
                        Ok(_) => (utf8.len(), 0),
                        Err(e) if e.error_len().is_none() => {
                            (e.valid_up_to(), utf8.len() - e.valid_up_to())
                        }
                        Err(_) => (utf8.len(), 0),
                    };
                    let converted = String::from_utf8_lossy(&utf8[..valid]);
                    pending.extend_from_slice(converted.as_bytes());
                    if left > 0 {
                        utf8.drain(..valid);
                    } else {
                        utf8.clear();
                    }
                    self.parse_output(&mut pending);
                }
                Err(e)
                    if matches!(
                        e.kind(),
                        std::io::ErrorKind::WouldBlock | std::io::ErrorKind::Interrupted
                    ) =>
                {
                    if self.exited.load(Ordering::SeqCst) {
                        break;
                    }
                    std::thread::sleep(Duration::from_millis(5));
                }
                Err(_) => break,
            }
        }
        self.append(&pending);
        if !utf8.is_empty() {
            self.append(String::from_utf8_lossy(&utf8).as_bytes());
        }
        while !self.exited.load(Ordering::SeqCst) {
            std::thread::sleep(Duration::from_millis(5));
        }
        let mut state = self.state.lock().unwrap();
        let interrupted = state.active && state.code.is_none();
        state.closed = true;
        state.active = false;
        if state.code.is_none() {
            state.code = Some(state.shell_code);
        }
        state.ended = Some(Instant::now());
        *self.activity.lock().unwrap() = Instant::now();
        self.changed.notify_all();
        if interrupted {
            self.reporter.finished(&state.command_id, state.shell_code);
        }
        drop(state);
        let _ = fs::remove_dir_all(&self.directory);
    }
    fn parse_output(&self, pending: &mut Vec<u8>) {
        loop {
            if let Some(at) = pending
                .windows(self.marker.len())
                .position(|w| w == self.marker)
            {
                if at > 0 {
                    self.append(&pending[..at]);
                    pending.drain(..at);
                }
                let rest = &pending[self.marker.len()..];
                let Some(end) = rest.iter().position(|b| *b == 31) else {
                    if rest.len() > 16 {
                        self.append(&pending[..1]);
                        pending.remove(0);
                        continue;
                    }
                    break;
                };
                let code = std::str::from_utf8(&rest[..end])
                    .ok()
                    .and_then(|s| s.parse::<i32>().ok());
                if let Some(code) = code {
                    pending.drain(..self.marker.len() + end + 1);
                    self.prompt(code);
                } else {
                    self.append(&pending[..1]);
                    pending.remove(0);
                }
            } else {
                let keep = (1..self.marker.len().min(pending.len() + 1))
                    .rev()
                    .find(|n| pending.ends_with(&self.marker[..*n]))
                    .unwrap_or(0);
                let n = pending.len() - keep;
                self.append(&pending[..n]);
                pending.drain(..n);
                break;
            }
        }
    }
    fn snapshot(&self, s: &State, cursor: u64, limit: usize, pin: &str) -> Result<Value> {
        if !pin.is_empty() && pin != s.command_id {
            return Err("session command changed".into());
        }
        let total = s.offset + s.output.len() as u64;
        if cursor > total {
            return Err("cursor is beyond available output".into());
        }
        let begin = cursor.max(s.offset);
        let start = (begin - s.offset) as usize;
        if start < s.output.len() && (s.output[start] & 0xc0) == 0x80 {
            return Err("cursor is not a UTF-8 boundary".into());
        }
        let mut end = (start + limit).min(s.output.len());
        while end < s.output.len() && (s.output[end] & 0xc0) == 0x80 {
            end -= 1;
        }
        let mut result = json!({"session_id":self.id,"command_id":s.command_id,"status":s.status(),"output":String::from_utf8_lossy(&s.output[start..end]),"next_cursor":s.offset+end as u64,"has_more":end<s.output.len()});
        if let Some(code) = s.code {
            result["exit_code"] = json!(code);
        }
        if cursor < s.offset {
            result["truncated"] = json!(true);
        }
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn manager() -> Manager {
        Manager::new(Arc::new(AtomicBool::new(false)))
    }
    fn call(m: &Manager, method: &str, params: Value) -> Result<Value> {
        m.dispatch(
            json!({"method":method,"params":params}),
            &AtomicBool::new(false),
        )
    }
    fn script<'a>(unix: &'a str, windows: &'a str) -> &'a str {
        if cfg!(windows) { windows } else { unix }
    }
    #[test]
    fn persistent_state_and_exit_code() {
        let m = manager();
        let a=call(&m,"execute",json!({"command":script("export AGENTERM_TEST=preserved; printf '你好\\n'", "$env:AGENTERM_TEST='preserved'; Write-Output '你好'"),"setup":{"session_id":"test"}})).unwrap();
        assert_eq!(a["status"], "idle");
        assert!(a["output"].as_str().unwrap().contains("你好"));
        let b = call(
            &m,
            "execute",
            json!({"command":script("printf '%s' \"$AGENTERM_TEST\"; false", "Write-Output $env:AGENTERM_TEST; cmd /c exit 1"),"session_id":"test"}),
        )
        .unwrap();
        assert_eq!(b["exit_code"], 1);
        assert!(b["output"].as_str().unwrap().contains("preserved"));
        m.close();
    }
    #[test]
    fn zero_wait_input_and_closed_history() {
        let m = manager();
        let a = call(
            &m,
            "execute",
            json!({"command":script("read answer; printf 'got:%s' \"$answer\"", "$answer=Read-Host; Write-Output ('got:'+$answer)"),"wait_ms":0}),
        )
        .unwrap();
        let id = a["session_id"].as_str().unwrap();
        assert_eq!(a["status"], "running");
        // Input may arrive immediately after admission, before shell startup.
        call(
            &m,
            "control",
            json!({"session_id":id,"action":"input","input":"hello\n"}),
        )
        .unwrap();
        // Read returns as soon as any bytes arrive. PowerShell can echo input
        // before the command's output, so drain until command completion.
        let mut output = String::new();
        let mut cursor = 0;
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let b = call(
                &m,
                "read",
                json!({"session_id":id,"cursor":cursor,"wait_ms":1000}),
            )
            .unwrap();
            output.push_str(b["output"].as_str().unwrap());
            cursor = b["next_cursor"].as_u64().unwrap();
            if (b["status"] == "idle" && b["has_more"] == false) || Instant::now() >= deadline {
                break;
            }
        }
        assert!(output.contains("got:hello"));
        let closed = call(&m, "control", json!({"session_id":id,"action":"stop"})).unwrap();
        assert_eq!(closed["status"], "closed");
        m.close();
    }
    #[test]
    fn invalid_or_canceled_requests_are_rejected() {
        let m = manager();
        let c = AtomicBool::new(true);
        assert!(
            m.dispatch(json!({"method":"list","params":{}}), &c)
                .is_err()
        );
        assert!(
            call(
                &m,
                "execute",
                json!({"command":"echo bad","session_id":"missing"})
            )
            .is_err()
        );
        assert!(call(&m, "execute", json!({"command":"echo bad","wait_ms":20001})).is_err());
        m.close();
    }

    #[test]
    #[cfg(windows)]
    fn shell_exit_closes_descendants_holding_stdout() {
        struct Cleanup(Manager);
        impl Drop for Cleanup {
            fn drop(&mut self) {
                self.0.close();
            }
        }
        let m = manager();
        let _cleanup = Cleanup(m.clone());
        let pid_file =
            std::env::temp_dir().join(format!("agenterm-child-{}.pid", random(12).unwrap()));
        let command = format!(
            "$p=Start-Process -FilePath $env:ComSpec -ArgumentList '/c','ping -n 31 127.0.0.1' -NoNewWindow -PassThru; [IO.File]::WriteAllText('{}',[string]$p.Id); exit 0",
            pid_file.to_string_lossy().replace('\'', "''")
        );
        let result = call(
            &m,
            "execute",
            json!({
                "command": command,
                "setup": {"session_id":"exiting-parent"},
                "wait_ms": 1000
            }),
        )
        .unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut status = result["status"].clone();
        let mut cursor = result["next_cursor"].as_u64().unwrap();
        while status != "closed" && Instant::now() < deadline {
            let result = call(
                &m,
                "read",
                json!({
                    "session_id":"exiting-parent", "cursor":cursor, "wait_ms":1000
                }),
            )
            .unwrap();
            status = result["status"].clone();
            cursor = result["next_cursor"].as_u64().unwrap();
        }
        let listed = call(&m, "list", json!({})).unwrap();
        assert_eq!(status, "closed", "{listed}");
        let child: u32 = fs::read_to_string(&pid_file)
            .expect("child must have started")
            .trim()
            .parse()
            .unwrap();
        let _ = fs::remove_file(pid_file);
        use windows_sys::Win32::{
            Foundation::{CloseHandle, ERROR_INVALID_PARAMETER, WAIT_OBJECT_0},
            System::Threading::{OpenProcess, PROCESS_SYNCHRONIZE, WaitForSingleObject},
        };
        unsafe {
            let handle = OpenProcess(PROCESS_SYNCHRONIZE, 0, child);
            if handle.is_null() {
                assert_eq!(
                    std::io::Error::last_os_error().raw_os_error(),
                    Some(ERROR_INVALID_PARAMETER as i32)
                );
            } else {
                let result = WaitForSingleObject(handle, 0);
                CloseHandle(handle);
                assert_eq!(result, WAIT_OBJECT_0, "descendant {child} is still running");
            }
        }
    }

    #[test]
    fn canceling_a_waiter_keeps_accepted_execution() {
        let m = manager();
        let cancel = Arc::new(AtomicBool::new(false));
        let worker_manager = m.clone();
        let worker_cancel = cancel.clone();
        let waiter = std::thread::spawn(move || {
            worker_manager.dispatch(
                json!({"method":"execute","params":{"command":script("sleep 0.2; printf survived", "Start-Sleep -Milliseconds 200; Write-Output survived"),"setup":{"session_id":"cancel"}}}),
                &worker_cancel,
            )
        });
        let deadline = Instant::now() + Duration::from_secs(10);
        while m.lookup("cancel").is_err() && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(1));
        }
        assert!(m.lookup("cancel").is_ok(), "execution was not accepted");
        cancel.store(true, Ordering::SeqCst);
        assert!(waiter.join().unwrap().is_err());
        let mut cursor = 0;
        let mut output = String::new();
        let deadline = Instant::now() + Duration::from_secs(10);
        loop {
            let r = call(
                &m,
                "read",
                json!({"session_id":"cancel","cursor":cursor,"wait_ms":1000}),
            )
            .unwrap();
            output.push_str(r["output"].as_str().unwrap());
            cursor = r["next_cursor"].as_u64().unwrap();
            if (r["status"] == "idle" && r["has_more"] == false) || Instant::now() > deadline {
                break;
            }
        }
        assert!(output.contains("survived"));
        m.close();
    }
}
