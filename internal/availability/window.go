package availability

import "time"

const horizonDays = 31

func QueryWindow(loc *time.Location, now time.Time) (start, end time.Time) {
	now = now.In(loc)
	start = startOfISOWeek(now, loc)
	end = start.AddDate(0, 0, horizonDays)
	return start, end
}

func startOfISOWeek(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	delta := (int(t.Weekday()) - int(time.Monday) + 7) % 7
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return midnight.AddDate(0, 0, -delta)
}
