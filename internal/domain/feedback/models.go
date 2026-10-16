package feedback

type Input struct {
	Authorized        bool
	Name              string
	LinkedInID        string
	Email             string
	Title             string
	Message           string
	RecaptchaResponse string
	ClientIP          string
	Attachments       []Attachment
}

type Attachment struct {
	Filename string
	Content  string
}
