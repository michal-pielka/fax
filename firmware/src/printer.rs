//! The thermal printer, over UART.
//!
//! Almost no ESC/POS knowledge lives here. The server renders the command bytes and
//! this writes them out unchanged, which is what keeps new formatting features
//! from needing a reflash. The exception is the paper query at the bottom:
//! it is a question rather than a document, and only the thing holding the
//! UART can hear the answer.

use std::time::Duration;

use esp_idf_svc::hal::delay::{TickType, BLOCK};
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
/// DTR is the busy line. Measured, it does not track the paper -- low with the
/// roll in and low with it out -- so has_paper asks the printer instead.
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

/// Ask the printer whether it has paper.
///
/// `None` means it did not answer, which is not the same as "no paper":
/// treating silence as empty would refuse every job the moment the return path
/// hiccups, so the caller keeps its last answer instead.
///
/// DLE EOT 4 is a real-time command -- the printer replies from an interrupt
/// rather than from behind the print queue -- so this is safe to ask mid-job.
pub fn has_paper(uart: &UartDriver) -> Option<bool> {
    // Anything already buffered arrived unprompted, and would be read as the
    // reply to this query.
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

/// Wait for the printer to finish, and learn about the paper on the way.
///
/// The trick is that GS r is *not* real-time: the printer executes it in the
/// order it comes out of the receive buffer, so its reply cannot arrive until
/// everything queued ahead of it has been printed. Sent straight after a job,
/// the reply is the completion signal.
///
/// That is the whole difference from has_paper, which uses DLE EOT 4 -- a
/// real-time command that answers immediately and would tell us nothing about
/// whether the job is done.
///
/// `Some(paper)` means the job finished and this is the state of the roll
/// afterwards. `None` means the printer never came back: out of paper mid-job,
/// jammed, or not listening.
pub fn wait_done(uart: &UartDriver, timeout: Duration) -> Option<bool> {
    write(uart, &[0x1D, 0x72, 0x01]).ok()?;

    let mut buf = [0u8; 1];
    if uart
        .read(
            &mut buf,
            TickType::new_millis(timeout.as_millis() as u64).ticks(),
        )
        .ok()?
        != 1
    {
        return None;
    }

    let status = buf[0];

    // GS r has no fixed bits to check, so this is the weaker sanity test: only
    // the low nibble is defined, and the two values this printer produces are
    // 0x00 with paper and 0x0c without.
    if status & 0b1111_0000 != 0 {
        log::warn!("printer answered {status:#04x}, not a paper status");
        return None;
    }

    // Bits 2 and 3 are the paper-end sensor.
    Some(status & 0b0000_1100 == 0)
}
