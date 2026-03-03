package phases

const (
	PoliticalLabelName            = "Auto/Political"
	CalendarLabelName             = "Auto/Calendar Reminder"
	PoliticalFilterQuery          = `"paid for by" OR "authorized by" OR "federal election commission" OR "f.e.c." OR "not authorized by any candidate"`
	CalendarAttachmentFilterQuery = "has:attachment (filename:ics OR filename:vcs)"
)
