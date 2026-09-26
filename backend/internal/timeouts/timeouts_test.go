package timeouts

import "testing"

// Each wait must outlast the one it contains, or an outer layer gives up
// while an inner one is still about to answer.
func TestTimeoutsNest(t *testing.T) {
	if !(Firmware < Ack && Ack < Print && Print < Write) {
		t.Errorf("firmware %v, ack %v, print %v, write %v do not nest", Firmware, Ack, Print, Write)
	}
}
