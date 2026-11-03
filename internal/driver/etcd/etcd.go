package etcd

import "cistern/internal/driver"

func init() {
	driver.Register("etcd", func(p driver.Params) (driver.Driver, error) {

		return driver.NewStub("etcd", p), nil
	})
}
