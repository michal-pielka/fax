//! The thermal printer, over UART.
//!
//! No ESC/POS knowledge lives here. The server renders the command bytes and
//! this writes them out unchanged, which is what keeps new formatting features
//! from needing a reflash.

use esp_idf_svc::hal::delay::BLOCK;
use esp_idf_svc::hal::gpio::{AnyIOPin, InputPin, OutputPin};
use esp_idf_svc::hal::uart::{config::Config as UartConfig, Uart, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::sys::EspError;

/// Wiring, from the printer's 5-pin TTL header:
///
///   pin 1 NC
///   pin 2 TX  (printer out) -> GPIO16
///   pin 3 RX  (printer in)  <- GPIO17
///   pin 4 DTR (printer out) -> GPIO23, unused
///   pin 5 GND
///
/// DTR is the busy/paper-out signal. It is not read yet: at 9600 baud the link
/// is far slower than the print engine, so the printer's buffer never fills.
/// It matters when real paper detection lands.
///
/// UART0 is the USB console, so the printer gets UART1.
pub fn open<'d, U: Uart + 'd>(
    uart: U,
    tx: impl OutputPin + 'd,
    rx: impl InputPin + 'd,
    baud: u32,
) -> Result<UartDriver<'d>, EspError> {
    UartDriver::new(
        uart,
        tx,
        rx,
        Option::<AnyIOPin>::None, // no CTS
        Option::<AnyIOPin>::None, // no RTS
        &UartConfig::default().baudrate(Hertz(baud)),
    )
}

/// Write a rendered job and block until it is physically on the wire.
pub fn write(uart: &UartDriver, data: &[u8]) -> Result<(), EspError> {
    // A single write may be partial, so loop until it has all gone out.
    let mut sent = 0;
    while sent < data.len() {
        sent += uart.write(&data[sent..])?;
    }

    // ESP-IDF buffers transmits and returns before the bytes reach the wire.
    // Without this we would report a receipt printed while it was still
    // sitting in a driver buffer.
    uart.wait_tx_done(BLOCK)
}
