//go:build browser

package browser

import "time"

// testLaunchDeadline replaces the product's 30 s launch deadline in tests: a
// test machine often runs many builds at once, and a launch that is merely slow
// there is not what a test is checking.
const testLaunchDeadline = 3 * time.Minute

// WithHeadless launches Chrome with --headless=new, so a test that needs no
// visible window opens none on the developer's screen, and allows a loaded
// machine a longer launch deadline. It exists only under the browser build tag;
// the launch contract forbids the flag in production.
func (o Options) WithHeadless() Options {
	o.headless = true
	o.launchTimeout = testLaunchDeadline
	return o
}
