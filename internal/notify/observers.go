package notify

import "context"

// Observers tells several observers of one board, in order.
type Observers []Observer

// Changed implements Observer.
func (o Observers) Changed(ctx context.Context, before *Entry, after Entry) {
	for _, x := range o {
		x.Changed(ctx, before, after)
	}
}

// Retired implements Observer.
func (o Observers) Retired(ctx context.Context, e Entry) {
	for _, x := range o {
		x.Retired(ctx, e)
	}
}
