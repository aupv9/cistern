package kafka

import "cistern/internal/driver"

func init() {
	driver.Register("kafka", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("kafka", p), nil
	})
}
