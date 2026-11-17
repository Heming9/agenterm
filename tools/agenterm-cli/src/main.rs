mod protocol;
mod storage;
mod terminal;
mod transport;
mod ui;

use protocol::{Result, Token};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

fn run() -> Result<()> {
    let Some(mut o) = ui::Options::parse()? else {
        return Ok(());
    };
    ui::heading();
    if o.invite.is_empty() && o.token.is_empty() {
        o.token = ui::input(ui::text(
            "Connection token, short code, or short-code address: ",
            "连接 Token / 短码：",
        ))?;
    }
    let invited = !o.invite.is_empty();
    let token = if invited {
        let invitation = Token::decode(&o.invite)?;
        if !o.consent {
            match ui::consent(&invitation.address.http().server())? {
                Some(temporary) => o.temporary = temporary,
                None => {
                    ui::line("Connection cancelled.", "已取消连接。");
                    return Ok(());
                }
            }
        }
        ui::line(
            "[3/4] Verifying invitation and connecting…",
            "[3/4] 正在验证邀请并连接服务器…",
        );
        let _reservation = storage::lock(&invitation.address)?;
        let t = transport::redeem(&invitation.invitation_url(), Some(&invitation.secret))?;
        if t.address.url() != invitation.address.url() {
            return Err("invitation resolved to a different server".into());
        }
        // Retain the reservation through redemption and connection startup.
        return connect(t, o.temporary, invited, _reservation);
    } else if o.token.starts_with("Agenterm_") {
        Token::decode(&o.token)?
    } else {
        let address = if protocol::short_code(&o.token) {
            let servers = storage::history()?;
            if servers.is_empty() {
                return Err(ui::text(
                    "No server history. Enter the full short-code address.",
                    "暂无历史服务器，请输入完整短码地址。",
                )
                .into());
            }
            ui::line("Select a server:", "选择历史服务器：");
            for (i, s) in servers.iter().enumerate() {
                println!("{}. {s}", i + 1);
            }
            let n = ui::input(ui::text("Server number: ", "服务器编号："))?
                .parse::<usize>()
                .map_err(|_| "invalid server number")?;
            let server = servers
                .get(n.checked_sub(1).ok_or("invalid server number")?)
                .ok_or("invalid server number")?;
            protocol::short_address(&format!("{server}/c/{}", o.token.to_ascii_uppercase()))?
        } else {
            protocol::short_address(&o.token)?
        };
        let mut target = address.clone();
        target.scheme = if address.secure() { "wss" } else { "ws" }.into();
        target.path = address.path.rsplit_once("/c/").unwrap().0.to_string() + "/client";
        let reservation = storage::lock(&target)?;
        let token = transport::redeem(&address, None)?;
        if token.address.url() != target.url() {
            return Err("short code resolved to a different server".into());
        }
        return connect(token, o.temporary, false, reservation);
    };
    let reservation = storage::lock(&token.address)?;
    connect(token, o.temporary, invited, reservation)
}

fn connect(
    token: Token,
    temporary: bool,
    invited: bool,
    _reservation: std::fs::File,
) -> Result<()> {
    if !invited {
        ui::line(
            "Connecting. Once connected, the server can execute commands with your current user permissions. Close this program to disconnect.",
            "正在连接。连接后，服务器可使用当前用户权限执行命令。关闭程序即可断开连接。",
        );
    }
    let stopped = Arc::new(AtomicBool::new(false));
    let signal = stopped.clone();
    ctrlc::set_handler(move || {
        signal.store(true, Ordering::SeqCst);
    })
    .map_err(|e| e.to_string())?;
    let manager = terminal::Manager::new(stopped.clone());
    let result = transport::run(&token, &manager, &stopped, temporary, invited);
    manager.close();
    result
}

fn main() {
    if let Err(e) = run() {
        eprintln!("[Agenterm] {e}");
        std::process::exit(1);
    }
}
