use crate::protocol::{Result, VERSION};
use std::io::{self, IsTerminal, Write};
use std::sync::OnceLock;

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
pub fn consent(server: &str) -> Result<Option<bool>> {
    println!(
        "{}\n\n{}{}\n{}\n",
        text("⚠️ Remote control authorization", "⚠️ 远程控制授权"),
        text("Server: ", "服务器："),
        server,
        text(
            "The server can control this computer with your current account permissions, including executing commands and accessing or modifying files. Make sure you trust the other party and understand the purpose of this connection.",
            "连接后，对方将能够以你当前账户的权限控制本机，包括执行命令、访问和修改文件。请确保你信任对方，并了解此次连接的用途。"
        )
    );
    line("Choose authorization:", "选择授权方式：");
    println!(
        "  1. {}\n  2. {}\n  3. {}",
        text("Allow authorization (temporary)", "允许授权（临时连接）"),
        text("Allow authorization (persistent)", "允许授权（持久连接）"),
        text("Deny authorization", "拒绝授权")
    );
    Ok(
        match input(text("Enter 1, 2 or 3: ", "请输入 1、2 或 3："))?.as_str() {
            "1" => Some(true),
            "2" => Some(false),
            _ => None,
        },
    )
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

#[derive(Default)]
pub struct Options {
    pub token: String,
    pub invite: String,
    pub temporary: bool,
    pub consent: bool,
}
impl Options {
    pub fn parse() -> Result<Option<Self>> {
        let mut o = Self {
            temporary: true,
            ..Self::default()
        };
        let mut args = std::env::args().skip(1);
        while let Some(arg) = args.next() {
            if !arg.starts_with('-') {
                return Err("unexpected positional argument; use -token or -invite".into());
            }
            let (flag, inline) = arg
                .split_once('=')
                .map_or((arg.as_str(), None), |(k, v)| (k, Some(v.to_string())));
            let flag = flag.trim_start_matches('-');
            match flag {
                "h" | "help" => {
                    println!(
                        "Agenterm {VERSION}\n\nUsage: agenterm-cli [-token TOKEN | -invite INVITATION]\n\n  -token VALUE              Connection token, short code, or short-code URL\n  -invite VALUE             One-time invitation (asks for authorization)\n  -connection-mode MODE     temporary (default) or persistent\n  -consent-confirmed        Installer has already obtained authorization\n  -plain                    Plain output (default in this prototype)\n  -version                  Print version\n\nAGENTERM_LANG=zh-CN selects Chinese. NO_COLOR disables colors."
                    );
                    return Ok(None);
                }
                "version" => {
                    println!("{VERSION}");
                    return Ok(None);
                }
                "plain" => {
                    if inline.as_deref().is_some_and(|v| v != "true") {
                        return Err("only plain output is supported".into());
                    }
                }
                "consent-confirmed" => {
                    o.consent = match inline.as_deref() {
                        None | Some("true") => true,
                        Some("false") => false,
                        _ => return Err("invalid boolean".into()),
                    };
                }
                "token" | "invite" | "connection-mode" => {
                    let v = inline
                        .or_else(|| args.next())
                        .ok_or_else(|| format!("-{flag} requires a value"))?;
                    match flag {
                        "token" => o.token = v.trim().into(),
                        "invite" => o.invite = v.trim().into(),
                        _ => {
                            o.temporary = match v.as_str() {
                                "temporary" => true,
                                "persistent" => false,
                                _ => {
                                    return Err(
                                        "-connection-mode must be temporary or persistent".into()
                                    );
                                }
                            }
                        }
                    }
                }
                _ => return Err(format!("unknown argument: {flag}")),
            }
        }
        if !o.token.is_empty() && !o.invite.is_empty() {
            return Err("-token and -invite cannot be used together".into());
        }
        if o.consent && o.invite.is_empty() {
            return Err("-consent-confirmed requires -invite".into());
        }
        Ok(Some(o))
    }
}
