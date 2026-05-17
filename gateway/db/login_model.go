package db

// LoginModel maps a single roamind user to one or more external channel
// accounts. _id is the roamind user_id. Each channel is an
// optional sub-document — new channels can be added as new fields.
type LoginModel struct {
	UserID   string           `bson:"_id"`
	Telegram *TelegramChannel `bson:"telegram,omitempty"`

	CreatedOn int64 `bson:"createdOn,omitempty"`
	UpdatedOn int64 `bson:"updatedOn,omitempty"`
}

// TelegramChannel holds a user's Telegram identity. ID is the Telegram
// user id; ChatID is the latest chat used to reach them.
type TelegramChannel struct {
	ID        int64  `bson:"id"`
	ChatID    int64  `bson:"chat_id"`
	Username  string `bson:"username,omitempty"`
	FirstName string `bson:"first_name,omitempty"`
	LastName  string `bson:"last_name,omitempty"`
}

// Id implements odm.DbModel.
func (l LoginModel) Id() string { return l.UserID }

// CollectionName implements odm.DbModel.
func (l LoginModel) CollectionName() string { return "login" }
