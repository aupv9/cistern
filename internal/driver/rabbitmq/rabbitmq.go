// Package rabbitmq là driver "rabbitmq". FR-10: definitions qua management API.
package rabbitmq

import "cistern/internal/driver"

func init() {
	driver.Register("rabbitmq", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("rabbitmq", p), nil
	})
}
