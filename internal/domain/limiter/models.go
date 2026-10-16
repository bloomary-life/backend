package limiter

type Type string

const (
	TypeFeedback    Type = "feedback"
	TypeToken       Type = "token"
	TypeCLIStart    Type = "cli_start"
	TypeCLIResult   Type = "cli_result"
	TypeEntitlement Type = "entitlement"
	TypeDraftExport Type = "draft_export"
)
