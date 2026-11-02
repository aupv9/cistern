// Package nats là driver "nats". FR-11: backup/restore từng JetStream stream.
package nats

import "cistern/internal/driver"

func init() {
	driver.Register("nats", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("nats", p), nil
	})
}
