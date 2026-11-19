use super::{log::welcome, *};
use ratatui::{backend::TestBackend, buffer::Buffer};
use serde::Deserialize;
use unicode_width::UnicodeWidthStr;

fn key(code: KeyCode) -> KeyEvent {
    KeyEvent::new(code, KeyModifiers::NONE)
}
fn ctrl_c() -> KeyEvent {
    KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL)
}
fn app() -> App {
    App::new(Options {
        temporary: true,
        ..Options::default()
    })
}
fn screen(app: &App) -> Vec<String> {
    let mut terminal = Terminal::new(TestBackend::new(app.width, app.height)).unwrap();
    terminal.draw(|frame| app.render(frame)).unwrap();
    buffer_rows(terminal.backend().buffer())
}
fn buffer_rows(buf: &Buffer) -> Vec<String> {
    (0..buf.area.height)
        .map(|y| {
            let mut row = String::new();
            let mut x = 0;
            while x < buf.area.width {
                let symbol = buf[(x, y)].symbol();
                row.push_str(symbol);
                x += (UnicodeWidthStr::width(symbol).max(1)) as u16;
            }
            row.trim_end().into()
        })
        .collect()
}
fn started(id: &str, command: &str) -> ClientEvent {
    ClientEvent::Started {
        id: id.into(),
        command: command.into(),
        time: "12:34:56".into(),
    }
}
fn output(id: &str, data: &str) -> ClientEvent {
    ClientEvent::Output {
        id: id.into(),
        data: data.into(),
    }
}
fn connected() -> ClientEvent {
    ClientEvent::Connected {
        elapsed: Duration::from_millis(241),
    }
}

#[test]
fn screens_match_reference() {
    #[derive(Deserialize)]
    struct Reference {
        version: String,
        screens: Vec<Screen>,
    }
    #[derive(Deserialize)]
    struct Screen {
        language: String,
        scenario: String,
        width: u16,
        height: u16,
        rows: Vec<String>,
    }
    let reference: Reference =
        serde_json::from_str(include_str!("../../tests/fixtures/tui.json")).unwrap();
    let (hash_head, hash_tail) = welcome::executable_hash()
        .split_once(welcome::HASH_SEPARATOR)
        .unwrap();
    for expected in reference.screens {
        if (expected.language == "zh-CN") != ui::zh() {
            continue;
        }
        let mut app = app();
        app.identity = "tester@workstation".into();
        app.resize(expected.width, expected.height);
        match expected.scenario.as_str() {
            "input" => {}
            "mode" => {
                app.input.set("example.com/c/ABC123".into());
                app.menu = Menu::Mode(0);
            }
            "server" => {
                app.input.set("ABC123".into());
                app.menu = Menu::Server(1);
                app.servers = vec![
                    "https://one.example/agenterm".into(),
                    "https://two.example/custom".into(),
                ];
            }
            "invalid" => {
                app.message = tr("invalid.agenterm.connection.address.or.token").into();
            }
            scenario => {
                app.address = "ws://127.0.0.1:18743/agenterm/client/abc123".into();
                app.state = State::Connected;
                app.latency = Some(42);
                if matches!(scenario, "connecting" | "connected" | "invitation") {
                    app.client_event(ClientEvent::Connecting(app.address.clone()));
                }
                if scenario == "invitation" {
                    app.progress = true;
                    app.client_event(ClientEvent::Redeemed {
                        token: protocol::Token {
                            encoded: "test-token".into(),
                            address: protocol::Address::parse(
                                "ws://127.0.0.1:18743/agenterm/client",
                            )
                            .unwrap(),
                            secret: "test-secret".into(),
                            identifier: "abc123".into(),
                        },
                        server_version: Some("0.20.8".into()),
                        elapsed: Duration::from_millis(86),
                    });
                }
                if matches!(scenario, "connected" | "invitation") {
                    app.client_event(ClientEvent::WebSocketUpgraded(101));
                    app.client_event(connected());
                    app.latency = (scenario == "connected").then_some(42);
                }
                if scenario == "reconnecting" {
                    app.state = State::Reconnecting(3);
                    app.latency = None;
                }
                if scenario == "log" || scenario == "expanded" {
                    app.client_event(started("one", "printf '你好 world\\n'"));
                    app.client_event(output("one", "\r\n你好 world\nsecond line\n\n"));
                    app.client_event(ClientEvent::Finished {
                        id: "one".into(),
                        code: 0,
                    });
                    app.refresh();
                    if scenario == "expanded" {
                        for row in (0..app.log.rows.len()).rev() {
                            app.log.toggle(row);
                        }
                        app.refresh();
                    }
                    app.follow = false;
                    app.scroll = 0;
                }
            }
        }
        app.refresh();
        let mut actual = screen(&app);
        for row in &mut actual {
            if matches!(
                row.as_bytes(),
                [
                    b'0'..=b'9',
                    b'0'..=b'9',
                    b':',
                    b'0'..=b'9',
                    b'0'..=b'9',
                    b':',
                    b'0'..=b'9',
                    b'0'..=b'9',
                    b' ',
                    b' ',
                    ..
                ]
            ) {
                row.replace_range(..8, "12:34:56");
            }
        }
        let expected_rows: Vec<String> = expected
            .rows
            .iter()
            .map(|s| {
                s.replace(
                    &format!("Agenterm v{}", reference.version),
                    &format!("Agenterm v{}", crate::protocol::VERSION),
                )
                .replace(".0000000", &format!(".{}", welcome::COMMIT))
                .replace("########......########", welcome::executable_hash())
                .replace("sha256: ########", &format!("sha256: {hash_head}"))
                .replace(
                    "......########",
                    &format!("{}{hash_tail}", welcome::HASH_SEPARATOR),
                )
            })
            .collect();
        assert_eq!(
            actual, expected_rows,
            "{} / {} / {} columns",
            expected.language, expected.scenario, expected.width
        );
    }
}

#[test]
fn long_identity_fits_the_footer_divider() {
    let mut app = app();
    app.identity = "用户@工作站-abcdefghijklmnopqrstuvwxyz".into();
    app.resize(28, 16);
    app.client_event(connected());
    app.latency = Some(42);

    let rows = screen(&app);
    assert_eq!(rows[13], " ─ 用户@工作站-abcdefghijk…");
    assert!(rows[14].ends_with(" · 42ms"));
}

#[test]
fn idle_reconnect_does_not_repeat_welcome() {
    let mut app = app();
    app.client_event(connected());
    app.refresh();
    assert!(screen(&app).join("\n").contains(tr("tui.welcome.waiting")));
    app.client_event(ClientEvent::Retry {
        message: "EOF".into(),
        delay: 1,
    });
    let address = "wss://example.com/agenterm/client/abc123";
    app.client_event(ClientEvent::Connecting(address.into()));
    app.refresh();
    assert_eq!(app.log.welcome_rows, 0);
    assert!(app.log.rows.iter().any(|row| row.text.ends_with(address)));
    app.client_event(connected());
    app.refresh();
    let restored = screen(&app).join("\n");
    assert!(restored.contains(tr("connection.restored")));
    assert!(!restored.contains(tr("tui.welcome.waiting")));
}

#[test]
fn startup_banner_is_selectable_in_the_log() {
    let mut app = app();
    app.resize(80, 16);
    assert_eq!(app.state, State::Disconnected);
    let [top, middle, bottom] = colors::TERMINAL_LOGO;
    let rows = screen(&app);
    assert!(rows[2].starts_with(&format!("  {top}")));
    assert!(rows[5].contains(tr("connection.token")));
    assert!(!rows.join("\n").contains(tr("tui.welcome.waiting")));

    for (kind, column, row) in [
        (MouseEventKind::Down(MouseButton::Left), 2, 2),
        (MouseEventKind::Drag(MouseButton::Left), 7, 4),
    ] {
        app.mouse(MouseEvent {
            kind,
            column,
            row,
            modifiers: KeyModifiers::NONE,
        });
    }
    let selected = app.selected_text();
    assert!(selected.starts_with(top));
    assert!(selected.contains(middle));
    assert!(selected.ends_with(bottom));

    app.client_event(connected());
    app.refresh();
    assert!(app.selection.is_none());
    assert!(screen(&app).join("\n").contains(tr("tui.welcome.waiting")));

    app.client_event(started("one", "pwd"));
    app.refresh();
    assert!(app.log.rows.iter().all(|row| !row.text.contains(top)));
    assert!(!screen(&app).join("\n").contains(tr("tui.welcome.waiting")));
}

#[test]
fn mode_cancel_and_ctrl_c_match_go() {
    let mut app = app();
    app.input.set("example.com/c/ABC123".into());
    app.key(key(KeyCode::Enter));
    assert_eq!(app.menu, Menu::Mode(0));
    assert!(app.worker.is_none());
    app.key(key(KeyCode::Right));
    assert_eq!(app.menu, Menu::Mode(1));
    app.key(key(KeyCode::Esc));
    assert_eq!(app.menu, Menu::Input);
    assert!(!app.input.value.is_empty());
    app.key(ctrl_c());
    assert!(app.input.value.is_empty());
    assert!(!app.exiting);
    app.key(ctrl_c());
    assert!(app.exiting);
}

#[test]
fn malformed_input_stays_editable_and_tiny_screens_do_not_panic() {
    let mut app = app();
    app.input.set("invalid".into());
    app.key(key(KeyCode::Enter));
    assert_eq!(app.state, State::Disconnected);
    assert!(app.worker.is_none());
    assert!(!app.message.is_empty());
    for width in [1, 2, 3, 5, 18, 25, 40, 80] {
        for height in [1, 2, 3, 5, 6, 7, 12] {
            app.resize(width, height);
            let _ = screen(&app);
        }
    }
}

#[test]
fn concurrent_streams_keep_go_spacing_and_decoders_survive_reconnect() {
    let mut app = app();
    app.state = State::Connected;
    app.resize(100, 40);
    app.client_event(started("one", "pwd"));
    app.client_event(output("one", "\r\n \r\nPath\r"));
    app.client_event(output("one", "\n----\r\nC:\\Users\\dmhe\r\n\r\n\r\n"));
    app.client_event(started("two", "printf test"));
    app.client_event(output("two", "test"));
    app.client_event(started("three", "next"));
    app.client_event(ClientEvent::Retry {
        message: "EOF".into(),
        delay: 1,
    });
    app.client_event(connected());
    app.client_event(output("one", "\n  after reconnect\n\n"));
    app.refresh();
    // Expand all output/command blocks and assert stream-edge blank lines were removed.
    for row in (0..app.log.rows.len()).rev() {
        app.log.toggle(row);
    }
    app.refresh();
    let text = app
        .log
        .rows
        .iter()
        .map(|r| r.text.as_str())
        .collect::<Vec<_>>()
        .join("\n");
    assert!(
        text.contains(
            "Path\n----\nC:\\Users\\dmhe\n\n12:34:56 $ printf test\ntest\n\n12:34:56 $ next"
        ),
        "{text}"
    );
    assert!(text.contains("  after reconnect"));
    assert!(!text.contains("Finished"));
}

#[test]
fn output_control_sequences_are_filtered_across_chunks() {
    let mut d = log::Decoder::default();
    assert_eq!(
        d.text("A\x1b[3")
            + &d.text("1m红\x1b]0;bad")
            + &d.text("\x07B\x1bPbad\x1b")
            + &d.text("\\C"),
        "A红BC"
    );
}

#[test]
fn selection_snaps_graphemes_joins_soft_wraps_and_freezes_output() {
    let mut app = app();
    app.resize(12, 18);
    app.state = State::Connected;
    app.client_event(started("one", "echo hi"));
    app.client_event(output("one", "你好👩‍💻abcde\nnext line\n"));
    app.refresh();
    for row in (0..app.log.rows.len()).rev() {
        app.log.toggle(row);
    }
    app.refresh();
    app.scroll = 0;
    app.follow = false;
    let start = app
        .log
        .rows
        .iter()
        .position(|r| r.text.contains("你好"))
        .unwrap();
    let end = start + 1;
    app.selection = Some(Selection {
        anchor: Point { row: start, col: 5 },
        cursor: Point { row: end, col: 2 },
        down: true,
        dragged: true,
        mouse_x: 0,
        mouse_y: 3,
        id: 1,
        request: 0,
        notice: "",
    });
    let text = app.selected_text();
    assert!(text.starts_with('好'), "{text:?}");
    assert!(!text.contains('\n'));
    let before = app
        .log
        .rows
        .iter()
        .map(|r| r.text.clone())
        .collect::<Vec<_>>();
    app.client_event(output("one", "more output\n"));
    app.refresh();
    assert_eq!(
        before,
        app.log
            .rows
            .iter()
            .map(|r| r.text.clone())
            .collect::<Vec<_>>()
    );
    app.clear_selection();
    assert_ne!(
        before,
        app.log
            .rows
            .iter()
            .map(|r| r.text.clone())
            .collect::<Vec<_>>()
    );
}

#[test]
fn resize_clears_selection_and_preserves_reading_offset() {
    let mut app = app();
    app.state = State::Connected;
    app.resize(60, 10);
    app.client_event(started("one", "echo hi"));
    app.client_event(output("one", &"line\n".repeat(50)));
    app.refresh();
    app.log.toggle(1);
    app.refresh();
    app.scroll_by(-3);
    let scroll = app.scroll;
    app.selection = Some(Selection {
        anchor: Point { row: 0, col: 0 },
        cursor: Point { row: 1, col: 0 },
        down: false,
        dragged: true,
        mouse_x: 0,
        mouse_y: 0,
        id: 1,
        request: 0,
        notice: "",
    });
    app.resize(50, 10);
    assert!(app.selection.is_none());
    assert_eq!(app.scroll, scroll.min(app.max_scroll()));
}

#[test]
fn bounded_logs_remain_utf8_and_eviction_invalidates_selection() {
    let mut app = app();
    app.state = State::Connected;
    app.client_event(started("one", "echo hi"));
    app.client_event(output("one", &"界".repeat(log::MAX_LOG / 3 + 10)));
    app.refresh();
    assert!(!app.log.rows.is_empty());
    assert!(
        app.log
            .rows
            .iter()
            .all(|r| r.text.is_char_boundary(r.text.len()))
    );
}

#[test]
fn grapheme_input_editing_and_paste_do_not_split_emoji() {
    let mut input = Input::new("a👩‍💻界".into());
    input.key(key(KeyCode::Left));
    input.key(key(KeyCode::Backspace));
    assert_eq!(input.value, "a界");
    input.insert("\x1b[31m\r\nx");
    assert!(!input.value.contains(['\x1b', '\n', '\r']));
    assert_eq!(Metrics { legacy: false }.width("👩‍💻界"), 4);
    assert_eq!(Metrics { legacy: true }.width("👩‍💻界"), 7);
}

#[test]
fn osc52_handles_plain_tmux_and_screen_terminals() {
    assert_eq!(
        clipboard::osc52("hello", false, false),
        "\x1b]52;c;aGVsbG8=\x07"
    );
    assert_eq!(
        clipboard::osc52("hello", true, false),
        "\x1bPtmux;\x1b\x1b]52;c;aGVsbG8=\x07\x1b\\"
    );
    assert_eq!(
        clipboard::osc52("hello", false, true),
        "\x1bP\x1b]52;c;aGVsbG8=\x07\x1b\\"
    );
}

#[test]
fn banner_uses_configured_colors_and_no_color_selection_reverses_text() {
    use ratatui::style::{Color, Modifier};

    let mut app = app();
    app.client_event(connected());
    app.refresh();
    app.palette = view::Palette { color: true };
    let mut terminal = Terminal::new(TestBackend::new(80, 24)).unwrap();
    terminal.draw(|frame| app.render(frame)).unwrap();
    let buffer = terminal.backend().buffer();
    assert_eq!(buffer[(0, 6)].fg, Color::from_u32(colors::SECONDARY));
    assert_eq!(buffer[(10, 6)].fg, Color::Reset);
    for position in [(2, 2), (10, 2), (2, 3), (2, 4)] {
        assert_eq!(buffer[position].fg, Color::from_u32(colors::PRIMARY));
    }
    for position in [(10, 3), (10, 4)] {
        assert_eq!(buffer[position].fg, Color::Reset);
    }

    app.palette = view::Palette { color: false };
    app.client_event(started("one", "echo hi"));
    app.client_event(output("one", "hello\n"));
    app.refresh();
    app.scroll = 0;
    app.follow = false;
    app.selection = Some(Selection {
        anchor: Point { row: 2, col: 0 },
        cursor: Point { row: 2, col: 2 },
        down: false,
        dragged: true,
        mouse_x: 0,
        mouse_y: 4,
        id: 1,
        request: 0,
        notice: "",
    });
    let mut terminal = Terminal::new(TestBackend::new(80, 24)).unwrap();
    terminal.draw(|frame| app.render(frame)).unwrap();
    assert!(
        terminal.backend().buffer()[(0, 4)]
            .modifier
            .contains(Modifier::REVERSED)
    );
}

#[test]
fn deleting_a_word_after_multibyte_whitespace_is_safe() {
    for separator in ['\u{3000}', '\u{a0}', '\u{2003}'] {
        let mut input = Input::new(format!("foo{separator}bar"));
        input.key(KeyEvent::new(KeyCode::Char('w'), KeyModifiers::CONTROL));
        assert_eq!(input.value, format!("foo{separator}"));
        assert_eq!(input.cursor, input.value.len());
    }
}

#[test]
fn folding_inside_a_long_block_keeps_the_clicked_block_visible() {
    let mut app = app();
    app.state = State::Connected;
    app.resize(60, 12);
    app.client_event(started("one", "echo lines"));
    app.client_event(output("one", &"line\n".repeat(120)));
    app.refresh();
    app.log.toggle(1);
    app.refresh();
    for i in 0..50 {
        app.client_event(started(&format!("tail{i}"), "tail command"));
    }
    app.refresh();
    app.scroll = 50;
    app.follow = false;
    let start = app.log.fold_start(app.scroll).unwrap();
    app.mouse(MouseEvent {
        kind: MouseEventKind::Down(MouseButton::Left),
        column: 0,
        row: 2,
        modifiers: KeyModifiers::NONE,
    });
    app.mouse(MouseEvent {
        kind: MouseEventKind::Up(MouseButton::Left),
        column: 0,
        row: 2,
        modifiers: KeyModifiers::NONE,
    });
    assert_eq!(app.scroll, start);
    assert!(app.log.rows[app.scroll].text.contains("[120"));
}

#[test]
fn clearing_selection_keeps_read_position_after_new_output() {
    let mut app = app();
    app.state = State::Connected;
    app.resize(60, 12);
    app.client_event(started("one", "echo lines"));
    app.client_event(output("one", &"line\n".repeat(20)));
    app.refresh();
    app.log.toggle(1);
    app.refresh();
    let before = app.scroll;
    app.selection = Some(Selection {
        anchor: Point {
            row: before,
            col: 0,
        },
        cursor: Point {
            row: before + 1,
            col: 2,
        },
        down: false,
        dragged: true,
        mouse_x: 0,
        mouse_y: 2,
        id: 1,
        request: 0,
        notice: "",
    });
    app.client_event(output("one", &"more\n".repeat(50)));
    app.refresh();
    assert_eq!(app.scroll, before);
    app.key(key(KeyCode::Esc));
    assert!(app.selection.is_none());
    assert_eq!(app.scroll, before);
    assert!(!app.follow);
}

#[test]
fn clicking_unfoldable_text_keeps_following_the_bottom() {
    let mut app = app();
    app.state = State::Connected;
    app.resize(60, 12);
    app.client_event(started("one", "echo hi"));
    app.client_event(output("one", "hello"));
    app.refresh();
    assert!(app.follow);
    app.mouse(MouseEvent {
        kind: MouseEventKind::Down(MouseButton::Left),
        column: 0,
        row: 2,
        modifiers: KeyModifiers::NONE,
    });
    app.mouse(MouseEvent {
        kind: MouseEventKind::Up(MouseButton::Left),
        column: 0,
        row: 2,
        modifiers: KeyModifiers::NONE,
    });
    assert!(app.follow);
}

#[test]
fn folded_markers_keep_default_color_while_timestamps_and_counts_are_dimmed() {
    let mut app = app();
    app.state = State::Connected;
    app.palette = view::Palette { color: true };
    app.resize(25, 16);
    app.client_event(started(
        "one",
        "a long command that wraps across several terminal rows",
    ));
    app.refresh();
    for expanded in [false, true] {
        if expanded {
            app.log.toggle(0);
            app.refresh();
        }
        app.scroll = 0;
        app.follow = false;
        let mut terminal = Terminal::new(TestBackend::new(25, 16)).unwrap();
        terminal.draw(|frame| app.render(frame)).unwrap();
        let buffer = terminal.backend().buffer();
        assert_eq!(buffer[(0, 2)].fg, ratatui::style::Color::Reset);
        assert_eq!(
            buffer[(2, 2)].fg,
            ratatui::style::Color::from_u32(colors::SECONDARY)
        );
    }
}

#[test]
fn screen_clipboard_payload_is_chunked_like_go_osc52() {
    // 120 ASCII bytes become 160 base64 bytes: 76 + 76 + 8.
    let text = "a".repeat(120);
    let chunk = "YWFh".repeat(19);
    let expected = format!("\x1bP\x1b]52;c;{chunk}\x1b\\\x1bP{chunk}\x1b\\\x1bPYWFhYWFh\x07\x1b\\");
    assert_eq!(clipboard::osc52(&text, false, true), expected);
}

#[test]
fn clearing_selection_does_not_cancel_a_requested_remote_copy() {
    let mut app = app();
    let sequence = clipboard::osc52("selected text", false, false);
    // The current clipboard request is zero before any real external copy;
    // delivering its completion exercises the UI without modifying a clipboard.
    let copied = clipboard::Copied {
        selection: 1,
        request: 0,
        notice: "tui.selection.sent",
        sequence: Some(sequence.clone()),
    };
    app.clear_selection();
    assert_eq!(app.copy_result(copied), Some(sequence));
    let obsolete = clipboard::Copied {
        selection: 1,
        request: u64::MAX,
        notice: "tui.selection.sent",
        sequence: Some("obsolete".into()),
    };
    assert!(app.copy_result(obsolete).is_none());
}
