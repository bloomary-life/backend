package resend

type Feedback struct {
	Name        string
	LinkedInID  string
	Email       string
	Title       string
	Message     string
	Attachments []Attachment
}

type Attachment struct {
	Filename string
	Content  string
}
