use crate::protocol::{Result, VERSION};
use std::fmt::Display;
use std::io::{self, IsTerminal, Write};
use std::sync::OnceLock;

mod reporter;
pub use reporter::{Event, Reporter, timestamp as reporter_timestamp};
include!(concat!(env!("OUT_DIR"), "/ui_messages.rs"));

pub fn zh() -> bool {
    static ZH: OnceLock<bool> = OnceLock::new();
    *ZH.get_or_init(|| {
        let lang = std::env::var("AGENTERM_LANG")
            .or_else(|_| std::env::var("LC_ALL"))
            .or_else(|_| std::env::var("LC_MESSAGES"))
            .or_else(|_| std::env::var("LANG"))
            .unwrap_or_default();
        lang.to_ascii_lowercase().starts_with("zh")
    })
}
pub fn text<'a>(en: &'a str, cn: &'a str) -> &'a str {
    if zh() { cn } else { en }
}
pub fn line(en: &str, cn: &str) {
    println!("{}", text(en, cn));
}
pub fn input(prompt: &str) -> Result<String> {
    print!("{prompt}");
    io::stdout().flush().map_err(|e| e.to_string())?;
    let mut s = String::new();
    if io::stdin().read_line(&mut s).map_err(|e| e.to_string())? == 0 {
        return Err(text("Input closed.", "输入已关闭。").into());
    }
    Ok(s.trim().into())
}
pub struct AuthorizationPrompt<'a> {
    pub title: &'static str,
    pub server_label: &'static str,
    pub server: &'a str,
    pub details: &'static str,
    pub instruction: &'static str,
}

impl AuthorizationPrompt<'_> {
    pub fn formatted(&self, title: impl Display, server: impl Display) -> String {
        format!(
            "{title}\n{}{server}\n{}\n\n{}",
            self.server_label, self.details, self.instruction
        )
    }
}

pub fn authorization_prompt(server: &str, interactive: bool) -> AuthorizationPrompt<'_> {
    AuthorizationPrompt {
        title: tr("remote.control.authorization.title"),
        server_label: tr("remote.control.authorization.server"),
        server,
        details: tr("remote.control.authorization.details"),
        instruction: if interactive {
            tr("installer.consent.menu")
        } else {
            text("Choose authorization:", "请选择授权方式：")
        },
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ConnectionMode {
    #[default]
    Temporary,
    Persistent,
}

pub fn consent_choices(mode: Option<ConnectionMode>) -> &'static [Option<ConnectionMode>] {
    use ConnectionMode::{Persistent, Temporary};
    match mode {
        None => &[Some(Temporary), Some(Persistent), None],
        Some(Temporary) => &[Some(Temporary), None],
        Some(Persistent) => &[Some(Persistent), None],
    }
}

pub fn consent_label(mode: Option<ConnectionMode>) -> &'static str {
    tr(match mode {
        Some(ConnectionMode::Temporary) => "installer.consent.temporary",
        Some(ConnectionMode::Persistent) => "installer.consent.persistent",
        None => "installer.consent.no",
    })
}

pub fn consent_row(mode: Option<ConnectionMode>, selected: bool) -> String {
    let marker = if selected { " ▶ " } else { "   " };
    format!("{marker}{} ", consent_label(mode))
}

pub fn consent(server: &str, mode: Option<ConnectionMode>) -> Result<Option<ConnectionMode>> {
    let interactive = io::stdin().is_terminal() && io::stdout().is_terminal();
    let prompt = authorization_prompt(server, interactive);
    if interactive {
        return crate::tui::consent(&prompt, mode);
    }
    println!("{}", prompt.formatted(prompt.title, prompt.server));
    let choices = consent_choices(mode);
    for (index, choice) in choices.iter().enumerate() {
        println!("{}. {}", index + 1, consent_label(*choice));
    }
    let answer = input(text("Enter a choice: ", "请输入选项编号："))?;
    Ok(answer
        .parse::<usize>()
        .ok()
        .and_then(|n| n.checked_sub(1))
        .and_then(|index| choices.get(index).copied().flatten()))
}
fn dim(s: &str) -> String {
    if io::stdout().is_terminal()
        && std::env::var_os("NO_COLOR").is_none()
        && std::env::var("TERM").unwrap_or_default() != "dumb"
    {
        format!("\x1b[90m{s}\x1b[0m")
    } else {
        s.into()
    }
}
pub fn heading() {
    println!("{}\n", dim(&format!("Agenterm · v{VERSION}")));
}
pub fn started(command: &str) {
    println!(
        "\n{} {command}",
        dim(text(
            "[Agenterm] Running (interactive):",
            "[Agenterm] 正在执行（交互）："
        ))
    );
}
pub fn finished(code: i32) {
    println!(
        "\n{}",
        dim(&if zh() {
            format!("[Agenterm] 执行结束（退出码：{code}）")
        } else {
            format!("[Agenterm] Finished (exit code: {code})")
        })
    );
}
pub fn output(data: &[u8]) {
    let mut out = io::stdout().lock();
    let _ = out.write_all(data);
    let _ = out.flush();
}

#[derive(Default, Clone)]
pub struct Options {
    pub token: String,
    pub mode: Option<ConnectionMode>,
    pub plain: bool,
    pub token_provided: bool,
}
impl Options {
    pub fn parse(args: impl IntoIterator<Item = String>) -> Result<Option<Self>> {
        let mut o = Self::default();
        let mut args = args.into_iter();
        while let Some(arg) = args.next() {
            if !arg.starts_with('-') {
                return Err("unexpected positional argument; use -token".into());
            }
            let (flag, inline) = arg
                .split_once('=')
                .map_or((arg.as_str(), None), |(k, v)| (k, Some(v.to_string())));
            let flag = flag.trim_start_matches('-');
            match flag {
                "h" | "help" => {
                    println!(
                        "Agenterm {VERSION}\n\nUsage: agenterm-cli [-token VALUE] [-connection-mode MODE]\n       agenterm-cli \"agenterm-cli://authorize?url=ENCODED_URL\"\n\n  -token VALUE              Connection token, short code, or invitation URL\n  -connection-mode MODE     temporary or persistent\n  -plain                    Plain output (automatic without a terminal)\n  -version                  Print version\n\nRemote control must be authorized locally before connecting.\nAGENTERM_LANG=zh-CN selects Chinese. NO_COLOR disables colors."
                    );
                    return Ok(None);
                }
                "version" => {
                    println!("{VERSION}");
                    return Ok(None);
                }
                "plain" => {
                    o.plain = boolean(inline.as_deref())?;
                }
                "token" | "connection-mode" => {
                    let v = inline
                        .or_else(|| args.next())
                        .ok_or_else(|| format!("-{flag} requires a value"))?;
                    match flag {
                        "token" => {
                            o.token_provided = true;
                            o.token = v.trim().into();
                        }
                        _ if v == "temporary" => o.mode = Some(ConnectionMode::Temporary),
                        _ if v == "persistent" => o.mode = Some(ConnectionMode::Persistent),
                        _ => return Err("-connection-mode must be temporary or persistent".into()),
                    }
                }
                _ => return Err(format!("unknown argument: {flag}")),
            }
        }
        Ok(Some(o))
    }
}

fn boolean(value: Option<&str>) -> Result<bool> {
    match value {
        None | Some("true") => Ok(true),
        Some("false") => Ok(false),
        _ => Err("invalid boolean".into()),
    }
}

#[test]
fn connection_mode_limits_choices_without_bypassing_consent() {
    use ConnectionMode::{Persistent, Temporary};
    let options = Options::parse(["-connection-mode".into(), "persistent".into()])
        .unwrap()
        .unwrap();
    assert_eq!(consent_choices(options.mode), &[Some(Persistent), None]);
    let options = Options::parse(["-connection-mode=temporary".into()])
        .unwrap()
        .unwrap();
    assert_eq!(consent_choices(options.mode), &[Some(Temporary), None]);
    assert_eq!(
        consent_choices(None),
        &[Some(Temporary), Some(Persistent), None]
    );
}
