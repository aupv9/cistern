// Package mysql là driver "mysql". FR-04: dump MySQL/MariaDB kèm routine, trigger, event.
package mysql

import "cistern/internal/driver"

func init() {
	driver.Register("mysql", func(p driver.Params) (driver.Driver, error) {
		// TODO: hiện thực driver thật.
		return driver.NewStub("mysql", p), nil
	})
}
