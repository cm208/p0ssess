package score

import "time"

type timeT = time.Time

// timeNow is swapped in tests.
var timeNow = time.Now
