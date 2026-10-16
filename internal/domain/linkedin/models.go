package linkedin

type Token struct {
	AccessToken string
	TokenType   string
	Scope       string
	ExpiresIn   int64
	StatusCode  int
}

type Profile struct {
	MemberID   string
	Name       string
	PictureURL string
	Email      string
}

type Post struct {
	MemberID   string
	Commentary string
	Visibility string
	MediaTitle string
	MediaURN   string
}

type Upload struct {
	Owner string
	Kind  UploadKind
}

type UploadKind string

const (
	UploadKindImage    UploadKind = "image"
	UploadKindDocument UploadKind = "document"
)
