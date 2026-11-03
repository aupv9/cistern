package elasticsearch

import "cistern/internal/driver"

func init() {
	driver.Register("elasticsearch", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("elasticsearch", p), nil
	})
}
