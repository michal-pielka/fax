//! The thermal printer, over UART. The server renders the commands, so new
//! formatting needs no reflash. The exception is the status queries below.

use std::time::{Duration, Instant};

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
        // The transmit buffer is the driver's, filled by us and drained by
        // an interrupt. The default is 256 bytes, a quarter second at 9600
        // baud, so a task holding the CPU that long starves the wire and the
        // paper stutters. Eight seconds of slack instead; a square photo
        // fits in it twice over.
        &UartConfig::default()
            .baudrate(Hertz(baud))
            .tx_fifo_size(8 * 1024)
            .rx_fifo_size(1024),
    )
}

/// Software flow control, which is what this printer has: XOFF when its
/// buffer is nearly full, XON when there is room again. Text never triggers
/// it; a dense photo can, since heating dark rows is slower than the wire.
const XON: u8 = 0x11;
const XOFF: u8 = 0x13;

/// Write a rendered job and block until it is physically on the wire,
/// pausing when the printer asks.
pub fn write(uart: &UartDriver, data: &[u8]) -> Result<(), EspError> {
    for chunk in data.chunks(256) {
        hold_if_asked(uart)?;

        // A single write may be partial, so loop until it has all gone out.
        let mut sent = 0;
        while sent < chunk.len() {
            sent += uart.write(&chunk[sent..])?;
        }
    }

    // ESP-IDF returns before the bytes reach the wire, so without this we
    // report a receipt printed from inside a driver buffer.
    uart.wait_tx_done(BLOCK)
}

/// Read whatever the printer has said since we last looked, and if the last
/// word was XOFF, wait for XON. Up to a point: a printer that never says go
/// again is jammed, and holding the job forever would help nobody.
fn hold_if_asked(uart: &UartDriver) -> Result<(), EspError> {
    let mut b = [0u8; 1];
    let mut paused = false;

    while uart.remaining_read()? > 0 && uart.read(&mut b, 0)? == 1 {
        match b[0] {
            XOFF => paused = true,
            XON => paused = false,
            _ => {}
        }
    }

    if !paused {
        return Ok(());
    }

    let deadline = Instant::now() + Duration::from_secs(5);
    while Instant::now() < deadline {
        if uart.read(&mut b, TickType::new_millis(50).ticks())? == 1 && b[0] == XON {
            return Ok(());
        }
    }

    log::warn!("printer asked us to wait and never said go; carrying on");
    Ok(())
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
/// never did: jammed, unplugged, or silent. The answer comes once the printer
/// has processed everything before the query, so for a photo, where it prints
/// as it reads, this is close to "done"; for text it is milliseconds.
///
/// The reply is one byte, but it is not the only byte that can arrive: the
/// printer also says XON and XOFF as its buffer empties and fills. Those, and
/// anything else that is not a status byte, are read past rather than
/// mistaken for a refusal.
pub fn wait_done(uart: &UartDriver, timeout: Duration) -> bool {
    if write(uart, &[0x1D, 0x72, 0x01]).is_err() {
        return false;
    }

    let deadline = Instant::now() + timeout;
    let mut buf = [0u8; 1];

    while let Some(left) = deadline.checked_duration_since(Instant::now()) {
        let Ok(1) = uart.read(&mut buf, TickType::new_millis(left.as_millis() as u64).ticks()) else {
            return false;
        };

        let status = buf[0];

        // GS r has no fixed bits, so this is weak: only the low nibble is
        // defined, and this printer answers 0x00 with paper and 0x0c without.
        if status == XON || status == XOFF || status & 0b1111_0000 != 0 {
            log::debug!("printer said {status:#04x} while we waited for a status");
            continue;
        }

        return true;
    }

    false
}
