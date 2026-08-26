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

/// Hardcoded, and a hostname rather than an IP on purpose: rebuilding or
/// moving the VPS is then a DNS change instead of a USB cable and a reflash.
pub const MQTT_URL: &str = "mqtts://fax.pielka.sh:8883";
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
