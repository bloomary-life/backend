package appleiap

type jwsHeader struct {
	Alg string   `json:"alg"`
	X5C []string `json:"x5c"`
}

type signedTransactionPayload struct {
	AppAccountToken       string `json:"appAccountToken"`
	BundleID              string `json:"bundleId"`
	Environment           string `json:"environment"`
	ExpiresDate           int64  `json:"expiresDate"`
	OriginalTransactionID string `json:"originalTransactionId"`
	ProductID             string `json:"productId"`
	RevocationDate        int64  `json:"revocationDate"`
	SignedDate            int64  `json:"signedDate"`
	TransactionID         string `json:"transactionId"`
}

type serverNotificationPayload struct {
	NotificationType string                 `json:"notificationType"`
	Subtype          string                 `json:"subtype"`
	Data             serverNotificationData `json:"data"`
}

type serverNotificationData struct {
	SignedTransactionInfo string `json:"signedTransactionInfo"`
}
