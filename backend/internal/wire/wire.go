// Package wire knows one thing: how long bytes take to reach the printer.
// Every timeout that waits on a print is this plus a margin, so text stays
// instant and a picture gets the seconds it actually needs.
package wire

import "time"

// Baud is the printer's serial rate, read off its self-test page. It is the
// throughput ceiling of the whole system: 960 bytes a second.
const Baud = 9600

// Time is how long n bytes spend on the UART: ten bits per byte -- start,
// eight data, stop -- at Baud.
func Time(n int) time.Duration {
	return time.Duration(n) * 10 * time.Second / Baud
}
