//! Everything you would change without changing behaviour.
//!
//! Credentials are baked in at build time, so a missing one is a compile
//! error rather than firmware that silently ships without them:
//!
//!   WIFI_SSID=... WIFI_PASS=... MQTT_PASS=... cargo run --release
//!
//! The broker address is not among them -- it is fixed infrastructure, not a
//! secret, so it is a constant below and cannot be flashed wrong.

use std::time::Duration;

pub const WIFI_SSID: &str = env!("WIFI_SSID");
pub const WIFI_PASS: &str = env!("WIFI_PASS");

/// Websockets, not plain MQTT, and behind Caddy on 443 rather than a broker
/// port of its own. Caddy is an HTTP proxy and cannot route plain MQTT, so
/// this is what lets the broker sit behind the one ingress -- and it makes the
/// certificate Caddy's problem rather than something to share between
/// containers and renew every ninety days.
///
/// A hostname rather than an IP on purpose: moving the VPS is then a DNS
/// change instead of a USB cable and a reflash.
pub const MQTT_URL: &str = "wss://fax.pielka.sh/mqtt";
pub const MQTT_PASS: &str = env!("MQTT_PASS");
pub const MQTT_USER: &str = "printer";
pub const CLIENT_ID: &str = "fax-printer-1";

/// Must match DEVICE_ID on the server. If these drift the broker accepts
/// publishes nobody is subscribed to and jobs vanish with no error anywhere,
/// which is why main logs them at boot.
pub const JOB_TOPIC: &str = "fax/printer-1/job/+";
pub const STATE_TOPIC: &str = "fax/printer-1/state";

/// A prefix: the job id is appended, so an ack lands on
/// fax/printer-1/ack/<id> and the dispatcher matches it by topic without
/// parsing the payload. Mirrors the shape of the job topic on purpose.
pub const ACK_TOPIC: &str = "fax/printer-1/ack";

/// The two states we can publish while connected. `paper` is measured now --
/// see printer::has_paper -- so the gateway's 409 means what it says.
pub const ONLINE: &[u8] = br#"{"online":true,"paper":true}"#;
pub const NO_PAPER: &[u8] = br#"{"online":true,"paper":false}"#;

/// The last will: published by the broker if we vanish without disconnecting.
pub const OFFLINE: &[u8] = br#"{"online":false,"paper":false}"#;

/// What an ack says. The reasons are codes rather than prose because the
/// dispatcher switches on them -- a sentence would mean matching strings.
pub const ACK_OK: &[u8] = br#"{"ok":true}"#;
pub const ACK_NO_PAPER: &[u8] = br#"{"ok":false,"error":"no_paper"}"#;
pub const ACK_NO_CONFIRM: &[u8] = br#"{"ok":false,"error":"no_confirmation"}"#;

/// Printed on the printer's self-test page (hold feed, then power on).
pub const BAUD_RATE: u32 = 9600;

/// How often to ask about the paper when nothing else is happening. It is also
/// the longest the main loop ever blocks waiting for a job.
pub const PAPER_POLL: Duration = Duration::from_secs(5);

/// How long to wait for the printer to say it has finished a job.
///
/// Thirty seconds is far more than text needs -- a full 32-column receipt is
/// under a second of engine time -- and is sized for images, where a raster
/// block is orders of magnitude more data at the same 9600 baud.
///
/// The dispatcher's ackTimeout is two seconds longer, so a printer that gives
/// up gets to say why rather than leaving the server to guess. Nothing above
/// that waits at all any more: printing is fire-and-forget over HTTP, and the
/// outcome reaches the browser on the event stream.
pub const PRINT_TIMEOUT: Duration = Duration::from_secs(30);
