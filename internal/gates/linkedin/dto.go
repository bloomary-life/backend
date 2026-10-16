package linkedin

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int64  `json:"expires_in"`
}

type profileResponse struct {
	MemberID   string `json:"sub"`
	Name       string `json:"name"`
	PictureURL string `json:"picture"`
	Email      string `json:"email"`
}
