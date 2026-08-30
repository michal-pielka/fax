//! Station-mode WiFi.

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::modem::WifiModemPeripheral;
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys::EspError;
use esp_idf_svc::wifi::{AuthMethod, BlockingWifi, ClientConfiguration, Configuration, EspWifi};

/// Connects and returns the guard. Keep it alive: dropping it powers down the
/// radio, and MQTT then fails with a DNS error that never mentions WiFi.
pub fn connect<'d, M: WifiModemPeripheral + 'd>(
    modem: M,
    sysloop: EspSystemEventLoop,
    nvs: EspDefaultNvsPartition,
    ssid: &str,
    password: &str,
) -> Result<BlockingWifi<EspWifi<'d>>, EspError> {
    let mut wifi = BlockingWifi::wrap(EspWifi::new(modem, sysloop.clone(), Some(nvs))?, sysloop)?;

    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: ssid.try_into().expect("WIFI_SSID longer than 32 bytes"),
        password: password.try_into().expect("WIFI_PASS longer than 64 bytes"),
        auth_method: AuthMethod::WPA2Personal,
        ..Default::default()
    }))?;

    wifi.start()?;
    log::info!("associating with {ssid}");
    wifi.connect()?;

    // Associating is not the same as being routable: DHCP has to finish first,
    // and connecting to the broker before it does fails.
    wifi.wait_netif_up()?;
    log::info!("wifi up, ip {}", wifi.wifi().sta_netif().get_ip_info()?.ip);

    Ok(wifi)
}
