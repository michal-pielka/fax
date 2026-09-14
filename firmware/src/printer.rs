//! The thermal printer, over UART. The server renders the commands, so new
//! formatting needs no reflash. The exception is the status queries below.

use std::time::{Duration, Instant};

use esp_idf_svc::hal::delay::{TickType, BLOCK};
use esp_idf_svc::hal::gpio::{AnyIOPin, Input, InputPin, OutputPin, PinDriver, Pull};
use esp_idf_svc::hal::uart::{config::Config as UartConfig, Uart, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::sys::{EspError, ESP_ERR_TIMEOUT};

/// Software flow control: XOFF when the printer's buffer is nearly full, XON
/// when there is room again. Never seen from this printer, but honoured if
/// it ever speaks.
const XON: u8 = 0x11;
const XOFF: u8 = 0x13;

/// Bytes handed to the driver at a time: a quarter second of wire, and the
/// granularity at which XOFF and the busy line are looked at.
const CHUNK: usize = 256;

/// The printer: its serial line, and its busy line.
pub struct Port<'d> {
    uart: UartDriver<'d>,
    /// DTR, which this printer raises while it is busy. Watched and counted,
    /// not obeyed: obeying it (as UART flow control) made printing stutter,
    /// because the printer's own buffer can take a whole job and print it in
    /// one motion, while a printer held to "send only when I say" is starved
    /// by a 9600 baud wire. What it is worth is telling us when the printer
    /// is actually working, which no status command does.
    busy: PinDriver<'d, Input>,
}

/// The 5-pin TTL header: 1 NC, 2 TX -> GPIO16, 3 RX <- GPIO17, 4 DTR ->
/// GPIO23, 5 GND. UART0 is the USB console.
pub fn open<'d, U: Uart + 'd>(
    uart: U,
    tx: impl OutputPin + 'd,
    rx: impl InputPin + 'd,
    busy: impl InputPin + 'd,
    baud: u32,
) -> Result<Port<'d>, EspError> {
    let uart = UartDriver::new(
        uart,
        tx,
        rx,
        Option::<AnyIOPin>::None, // DTR is read as a plain input, see Port::busy
        Option::<AnyIOPin>::None, // no RTS: we never tell the printer to wait
        // The transmit buffer is the driver's, filled by us and drained by
        // an interrupt. The default is 256 bytes, a quarter second at 9600
        // baud, so a task holding the CPU that long starves the wire and the
        // paper stutters. Eight seconds of slack instead; a square photo
        // fits in it twice over.
        &UartConfig::default()
            .baudrate(Hertz(baud))
            .tx_fifo_size(8 * 1024)
            .rx_fifo_size(1024),
    )?;

    // Floating: the printer drives this line, and a pull would bias it.
    let busy = PinDriver::input(busy, Pull::Floating)?;
    log::info!("DTR idle: {}", if busy.is_high() { "HIGH" } else { "low" });

    Ok(Port { uart, busy })
}

impl Port<'_> {
    /// Write a rendered job and block until it is physically on the wire,
    /// pausing when the printer asks. Reports what the busy line did.
    pub fn write(&self, data: &[u8]) -> Result<(), EspError> {
        let mut watch = BusyWatch::new(&self.busy);

        for chunk in data.chunks(CHUNK) {
            self.hold_if_asked()?;

            // Never hand the driver more than its buffer has room for. Given
            // a full buffer, ESP-IDF's write spins on the free-space count
            // instead of sleeping, which starves the idle task and trips the
            // watchdog after five seconds -- a photo takes twenty. Waiting
            // here, with a real sleep, keeps the CPU shared for the job.
            while self.uart.remaining_write()? < chunk.len() {
                watch.look();
                std::thread::sleep(Duration::from_millis(20));
            }

            // A single write may be partial, so loop until it has all gone out.
            let mut sent = 0;
            while sent < chunk.len() {
                sent += self.uart.write(&chunk[sent..])?;
            }

            watch.look();
        }

        // ESP-IDF returns before the bytes reach the wire, so without this we
        // report a receipt printed from inside a driver buffer.
        self.uart.wait_tx_done(BLOCK)?;
        watch.report("while sending");

        Ok(())
    }

    /// Read whatever the printer has said since we last looked, and if the
    /// last word was XOFF, wait for XON. Up to a point: a printer that never
    /// says go again is jammed, and holding the job forever would help nobody.
    fn hold_if_asked(&self) -> Result<(), EspError> {
        let mut b = [0u8; 1];
        let mut paused = false;

        while self.uart.remaining_read()? > 0 && self.uart.read(&mut b, 0)? == 1 {
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
            // A slice with nothing in it is reported as a timeout error, not
            // as zero bytes; it is not a failure, only more waiting.
            match self.uart.read(&mut b, TickType::new_millis(50).ticks()) {
                Ok(1) if b[0] == XON => return Ok(()),
                Ok(_) => {}
                Err(e) if e.code() == ESP_ERR_TIMEOUT => {}
                Err(e) => return Err(e),
            }
        }

        log::warn!("printer asked us to wait and never said go; carrying on");
        Ok(())
    }

    /// Ask whether the printer has paper. `None` is "did not answer", not
    /// "no paper". DLE EOT 4 is real-time: the printer answers at once, even
    /// mid-job.
    pub fn has_paper(&self) -> Option<bool> {
        // Anything already buffered would be read as this query's reply.
        self.uart.clear_rx().ok()?;
        self.write(&[0x10, 0x04, 0x04]).ok()?;

        let mut buf = [0u8; 1];
        if self
            .uart
            .read(&mut buf, TickType::new_millis(300).ticks())
            .ok()?
            != 1
        {
            return None;
        }

        let status = buf[0];

        // Bits 1 and 4 are set in every status byte. If they are not, this is
        // a stray byte and guessing from it is worse than admitting we do not
        // know.
        if status & 0b0001_0010 != 0b0001_0010 {
            log::warn!("printer answered {status:#04x}, not a status byte");
            return None;
        }

        // Bits 5 and 6 are the paper-end sensor: 00 present, 11 gone.
        // Measured on this printer as 0x12 with the roll in, 0x72 with it out.
        Some(status & 0b0110_0000 == 0)
    }

    /// Wait for the printer to answer for the job just written. `false` means
    /// it never did: jammed, unplugged, or silent. The answer comes once the
    /// printer has parsed everything before the query, which for this printer
    /// is well before the paper stops, even for a photo. The busy line is
    /// watched meanwhile, since that is when it says the most.
    ///
    /// The reply is one byte, but it is not the only byte that can arrive:
    /// XON and XOFF, and anything else that is not a status byte, are read
    /// past rather than mistaken for a refusal.
    pub fn wait_done(&self, timeout: Duration) -> bool {
        if self.write(&[0x1D, 0x72, 0x01]).is_err() {
            return false;
        }

        let mut watch = BusyWatch::new(&self.busy);
        let deadline = Instant::now() + timeout;
        let mut buf = [0u8; 1];

        let answered = loop {
            let Some(left) = deadline.checked_duration_since(Instant::now()) else {
                break false;
            };

            // Short reads, so the busy line is sampled while waiting. An
            // empty slice comes back as a timeout error: that is the normal
            // case for most of the wait, not a reason to give up.
            let slice = left.min(Duration::from_millis(20));
            let n = match self
                .uart
                .read(&mut buf, TickType::new_millis(slice.as_millis() as u64).ticks())
            {
                Ok(n) => n,
                Err(e) if e.code() == ESP_ERR_TIMEOUT => 0,
                Err(_) => break false,
            };

            watch.look();

            if n != 1 {
                continue;
            }

            let status = buf[0];

            // GS r has no fixed bits, so this is weak: only the low nibble is
            // defined, and this printer answers 0x00 with paper and 0x0c
            // without.
            if status == XON || status == XOFF || status & 0b1111_0000 != 0 {
                log::debug!("printer said {status:#04x} while we waited for a status");
                continue;
            }

            break true;
        };

        watch.report("while waiting for the status");
        answered
    }
}

/// Counts what the busy line does over a stretch of time: how often it went
/// high and for how long in total. Sampled, not interrupt-driven, so a pulse
/// shorter than the gap between looks is missed; the point is the shape of
/// it, not every edge.
struct BusyWatch<'a> {
    pin: &'a PinDriver<'a, Input>,
    high: bool,
    rises: u32,
    since: Instant,
    total: Duration,
    started: Instant,
}

impl<'a> BusyWatch<'a> {
    fn new(pin: &'a PinDriver<'a, Input>) -> Self {
        let now = Instant::now();
        let high = pin.is_high();

        Self { pin, high, rises: 0, since: now, total: Duration::ZERO, started: now }
    }

    fn look(&mut self) {
        let now = self.pin.is_high();

        if now && !self.high {
            self.rises += 1;
            self.since = Instant::now();
        } else if !now && self.high {
            self.total += self.since.elapsed();
        }

        self.high = now;
    }

    fn report(&mut self, when: &str) {
        self.look();
        if self.high {
            self.total += self.since.elapsed();
        }

        log::info!(
            "DTR {when}: high {} times, {} ms of {} ms, {} now",
            self.rises,
            self.total.as_millis(),
            self.started.elapsed().as_millis(),
            if self.high { "HIGH" } else { "low" },
        );
    }
}
