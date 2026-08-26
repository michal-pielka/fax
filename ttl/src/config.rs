//! Everything you would change without changing behaviour.
//!
//! Credentials are baked in at build time, so a missing one is a compile
//! error rather than firmware that silently ships without them:
//!
//!   WIFI_SSID=... WIFI_PASS=... MQTT_PASS=... cargo run --release
//!
//! The broker address is not among them -- it is fixed infrastructure, not a
//! secret, so it is a constant below and cannot be flashed wrong.

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

/// Sent retained on connect. `paper` is asserted, not measured -- DTR on
/// GPIO23 is wired but unread -- and has to claim true, because the gateway
/// refuses to publish to a printer it believes is empty.
pub const ONLINE: &[u8] = br#"{"online":true,"paper":true}"#;

/// The last will: published by the broker if we vanish without disconnecting.
pub const OFFLINE: &[u8] = br#"{"online":false,"paper":false}"#;

/// Printed on the printer's self-test page (hold feed, then power on).
pub const BAUD_RATE: u32 = 9600;
