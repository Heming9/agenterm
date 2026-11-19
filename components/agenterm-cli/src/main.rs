mod connection;
mod deeplink;
mod p2p;
mod protocol;
mod storage;
mod terminal;
mod transport;
mod tui;
mod ui;

use protocol::{Result, Token};
use std::{
    io::{self, IsTerminal},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
};

fn run() -> Result<()> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let link_launch = deeplink::is_launch(&args);
    let args = deeplink::arguments(args)?;
    let options = ui::Options::parse(args)?;
    if !link_launch
        && !options
            .as_ref()
            .is_some_and(|options| options.temporary_assistance)
        && std::env::var_os("AGENTERM_SKIP_LINK_REGISTRATION").as_deref()
            != Some(std::ffi::OsStr::new("1"))
        && let Err(error) = deeplink::register()
    {
        eprintln!("Agenterm link registration: {error}");
    }
    let Some(mut options) = options else {
        return Ok(());
    };
    let workspace = options
        .temporary_assistance
        .then(storage::assistance_workspace)
        .transpose()?;
    if workspace.is_some() {
        Token::decode(&options.token)?;
    }
    if !options.invite.is_empty() {
        let invitation = Token::decode(&options.invite)?;
        if !options.consent {
            match ui::consent(&invitation.address.http().server())? {
                Some(temporary) => options.temporary = temporary,
                None => {
                    ui::line("Connection cancelled.", "已取消连接。");
                    return Ok(());
                }
            }
        }
    }
    let stopped = Arc::new(AtomicBool::new(false));
    if !options.plain && io::stdin().is_terminal() && io::stdout().is_terminal() {
        install_signal(stopped.clone())?;
        return tui::run(options, stopped, workspace);
    }
    if !options.invite.is_empty() {
        ui::line(
            "[3/4] Verifying invitation and connecting…",
            "[3/4] 正在验证邀请并连接服务器…",
        );
    }
    ui::heading();
    let invited = !options.invite.is_empty();
    if invited {
        options.token = options.invite;
    } else {
        if options.token.is_empty() && !options.token_provided {
            options.token = ui::input(ui::text(
                "Connection token, short code, or short-code address: ",
                "连接 Token / 短码：",
            ))?;
        }
        if protocol::short_code(&options.token) {
            let servers = storage::history()?;
            if servers.is_empty() {
                return Err(ui::tr("history.server.empty").into());
            }
            println!("{}", ui::tr("select.history.server"));
            for (i, s) in servers.iter().enumerate() {
                println!("{}. {s}", i + 1);
            }
            let n = ui::input(ui::text("Server number: ", "服务器编号："))?
                .parse::<usize>()
                .map_err(|_| "invalid server number")?;
            let server = servers
                .get(n.checked_sub(1).ok_or("invalid server number")?)
                .ok_or("invalid server number")?;
            options.token = format!("{server}/c/{}", options.token.to_ascii_uppercase());
        }
        ui::line(
            "Connecting. Once connected, the server can execute commands with your current user permissions. Close this program to disconnect.",
            "正在连接。连接后，服务器可使用当前用户权限执行命令。关闭程序即可断开连接。",
        );
    }
    install_signal(stopped.clone())?;
    connection::run(
        &options.token,
        invited,
        options.temporary,
        stopped,
        ui::Reporter::default(),
        workspace,
    )
}

fn install_signal(stopped: Arc<AtomicBool>) -> Result<()> {
    ctrlc::set_handler(move || stopped.store(true, Ordering::SeqCst)).map_err(|e| e.to_string())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("[Agenterm] {e}");
        std::process::exit(1);
    }
}
