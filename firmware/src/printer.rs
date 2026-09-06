//! The thermal printer, over UART. The server renders the commands, so new
//! formatting needs no reflash. The exception is the status queries below.

use std::time::Duration;

use esp_idf_svc::hal::delay::{TickType, BLOCK};
use esp_idf_svc::hal::gpio::{AnyIOPin, InputPin, OutputPin};
use esp_idf_svc::hal::uart::{config::Config as UartConfig, Uart, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::sys::EspError;

/// The 5-pin TTL header: 1 NC, 2 TX -> GPIO16, 3 RX <- GPIO17, 4 DTR ->
/// GPIO23 (measured; does not track paper), 5 GND. UART0 is the USB console.
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

    // ESP-IDF returns before the bytes reach the wire, so without this we
    // report a receipt printed from inside a driver buffer.
    uart.wait_tx_done(BLOCK)
}

/// Ask whether the printer has paper. `None` is "did not answer", not "no
/// paper". DLE EOT 4 is real-time: the printer answers at once, even mid-job.
pub fn has_paper(uart: &UartDriver) -> Option<bool> {
    // Anything already buffered would be read as this query's reply.
    uart.clear_rx().ok()?;
    write(uart, &[0x10, 0x04, 0x04]).ok()?;

    let mut buf = [0u8; 1];
    if uart
        .read(&mut buf, TickType::new_millis(300).ticks())
        .ok()?
        != 1
    {
        return None;
    }

    let status = buf[0];

    // Bits 1 and 4 are set in every status byte. If they are not, this is a
    // stray byte and guessing from it is worse than admitting we do not know.
    if status & 0b0001_0010 != 0b0001_0010 {
        log::warn!("printer answered {status:#04x}, not a status byte");
        return None;
    }

    // Bits 5 and 6 are the paper-end sensor: 00 present, 11 gone. Measured on
    // this printer as 0x12 with the roll in, 0x72 with it out.
    Some(status & 0b0110_0000 == 0)
}

/// Wait for the printer to answer for the job just written. `false` means it
/// never did: jammed, unplugged, or silent. The answer arrives once the bytes
/// have been parsed, which on this printer is well before the paper stops.
pub fn wait_done(uart: &UartDriver, timeout: Duration) -> bool {
    if write(uart, &[0x1D, 0x72, 0x01]).is_err() {
        return false;
    }

    let mut buf = [0u8; 1];
    let Ok(1) = uart.read(
        &mut buf,
        TickType::new_millis(timeout.as_millis() as u64).ticks(),
    ) else {
        return false;
    };

    let status = buf[0];

    // GS r has no fixed bits, so this is weak: only the low nibble is
    // defined, and this printer answers 0x00 with paper and 0x0c without.
    if status & 0b1111_0000 != 0 {
        log::warn!("printer answered {status:#04x}, not a paper status");
        return false;
    }

    true
}
