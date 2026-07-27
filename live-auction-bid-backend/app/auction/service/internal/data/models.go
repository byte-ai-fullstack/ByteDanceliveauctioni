package data

import "time"

type AuctionRoomModel struct {
	ID                  string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID       string `gorm:"column:main_account_id;type:varchar(64);not null;uniqueIndex:uidx_room_main_account;index:idx_room_main_status,priority:1"`
	Name                string `gorm:"column:name;type:varchar(128);not null"`
	Platform            string `gorm:"column:platform;type:varchar(32);not null;default:'douyin'"`
	PlatformRoomID      string `gorm:"column:platform_room_id;type:varchar(128);not null;default:'';index:idx_platform_room"`
	LiveSourceURL       string `gorm:"column:live_source_url;type:varchar(512);not null;default:''"`
	LiveStartedAtUnixMs int64  `gorm:"column:live_started_at_unix_ms;not null;default:0"`
	Status              string `gorm:"column:status;type:varchar(32);not null;default:'ACTIVE';index:idx_room_main_status,priority:2"`
	CreatedByUserID     string `gorm:"column:created_by_user_id;type:varchar(64);not null;default:'';index:idx_room_created_by"`
	CreatedAtUnixMs     int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs     int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (AuctionRoomModel) TableName() string { return "auction_rooms" }

type AuctionRoomStateModel struct {
	RoomID            string `gorm:"column:room_id;type:varchar(64);primaryKey"`
	MainAccountID     string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_room_state_main"`
	ActiveLotID       string `gorm:"column:active_lot_id;type:varchar(64);not null;default:''"`
	DisplayLotID      string `gorm:"column:display_lot_id;type:varchar(64);not null;default:''"`
	ActiveLotVersion  int64  `gorm:"column:active_lot_version;not null;default:0"`
	NextQueuePosition int32  `gorm:"column:next_queue_position;type:int;not null;default:1"`
	UpdatedAtUnixMs   int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (AuctionRoomStateModel) TableName() string { return "auction_room_states" }

type AuctionLotModel struct {
	ID                     string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID          string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_lot_main_room_status,priority:1;index:idx_lot_main_updated,priority:1;index:idx_lot_main_room_queue,priority:1"`
	RoomID                 string `gorm:"column:room_id;type:varchar(64);not null;index:idx_room_status,priority:1;index:idx_room_updated,priority:1;index:idx_lot_main_room_status,priority:2;index:idx_lot_main_room_queue,priority:2"`
	Title                  string `gorm:"column:title;type:varchar(255);not null"`
	Description            string `gorm:"column:description;type:text;not null"`
	ImageURL               string `gorm:"column:image_url;type:varchar(1024);not null"`
	Status                 int32  `gorm:"column:status;type:int;not null;index:idx_room_status,priority:2;index:idx_status_ends_at,priority:1;index:idx_lot_main_room_status,priority:3"`
	QueueStatus            int32  `gorm:"column:queue_status;type:int;not null;default:1;index:idx_room_queue,priority:2;index:idx_lot_main_room_queue,priority:3"`
	QueuePosition          int32  `gorm:"column:queue_position;type:int;not null;default:0;index:idx_room_queue,priority:3;index:idx_lot_main_room_queue,priority:4"`
	Currency               string `gorm:"column:currency;type:char(3);not null;default:'CNY'"`
	StartPriceAmount       int64  `gorm:"column:start_price_amount;not null"`
	MinIncrementAmount     int64  `gorm:"column:min_increment_amount;not null"`
	CapPriceAmount         *int64 `gorm:"column:cap_price_amount"`
	DurationSeconds        int32  `gorm:"column:duration_seconds;type:int;not null"`
	AntiSnipeWindowSeconds int32  `gorm:"column:anti_snipe_window_seconds;type:int;not null"`
	AntiSnipeExtendSeconds int32  `gorm:"column:anti_snipe_extend_seconds;type:int;not null"`
	MaxExtendCount         int32  `gorm:"column:max_extend_count;type:int;not null"`
	CurrentPriceAmount     int64  `gorm:"column:current_price_amount;not null"`
	LeadingUserID          string `gorm:"column:leading_user_id;type:varchar(64);not null;default:''"`
	LeadingNickname        string `gorm:"column:leading_nickname;type:varchar(128);not null;default:''"`
	StartedAtUnixMs        int64  `gorm:"column:started_at_unix_ms;not null;default:0"`
	EndsAtUnixMs           int64  `gorm:"column:ends_at_unix_ms;not null;default:0;index:idx_status_ends_at,priority:2"`
	SettledAtUnixMs        int64  `gorm:"column:settled_at_unix_ms;not null;default:0"`
	CancelReason           string `gorm:"column:cancel_reason;type:varchar(512);not null;default:''"`
	CancelledAtUnixMs      int64  `gorm:"column:cancelled_at_unix_ms;not null;default:0"`
	WinnerUserID           string `gorm:"column:winner_user_id;type:varchar(64);not null;default:''"`
	WinnerNickname         string `gorm:"column:winner_nickname;type:varchar(128);not null;default:''"`
	FinalPriceAmount       int64  `gorm:"column:final_price_amount;not null;default:0"`
	Version                int64  `gorm:"column:version;not null"`
	ConfigVersion          int64  `gorm:"column:config_version;not null;default:1"`
	PlaybookStage          int32  `gorm:"column:playbook_stage;type:int;not null"`
	Payload                string `gorm:"column:payload;type:json;not null"`
	CreatedAt              time.Time
	UpdatedAt              time.Time `gorm:"index:idx_room_updated,priority:2;index:idx_lot_main_updated,priority:2"`
}

func (AuctionLotModel) TableName() string { return "auction_lots" }

// AuctionLotPresentationModel is an independently versioned live-display
// aggregate. It must never be used to adjudicate bids or advance lot.version.
type AuctionLotPresentationModel struct {
	LotID           string `gorm:"column:lot_id;type:varchar(64);primaryKey"`
	MainAccountID   string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_lot_presentation_main"`
	Version         int64  `gorm:"column:version;not null"`
	Payload         string `gorm:"column:payload;type:json;not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionLotPresentationModel) TableName() string { return "auction_lot_presentations" }

type AuctionBidModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID   string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_bid_main_lot_created,priority:1"`
	LotID           string `gorm:"column:lot_id;type:varchar(64);not null;index:idx_lot_created,priority:1;index:idx_lot_amount,priority:1;index:idx_lot_user,priority:1;index:idx_bid_main_lot_created,priority:2;uniqueIndex:idx_lot_user_idem,priority:1"`
	UserID          string `gorm:"column:user_id;type:varchar(64);not null;index:idx_lot_user,priority:2;uniqueIndex:idx_lot_user_idem,priority:2"`
	Nickname        string `gorm:"column:nickname;type:varchar(128);not null"`
	Amount          int64  `gorm:"column:amount;not null;index:idx_lot_amount,priority:2"`
	Currency        string `gorm:"column:currency;type:varchar(16);not null"`
	IdempotencyKey  string `gorm:"column:idempotency_key;type:varchar(128);not null;uniqueIndex:idx_lot_user_idem,priority:3"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null;index:idx_lot_created,priority:2;index:idx_bid_main_lot_created,priority:3"`
	Payload         string `gorm:"column:payload;type:json;not null"`
	CreatedAt       time.Time
}

func (AuctionBidModel) TableName() string { return "auction_bids" }

type AuctionLotStatsModel struct {
	LotID            string `gorm:"column:lot_id;type:varchar(64);primaryKey"`
	MainAccountID    string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_lot_stats_main_room,priority:1"`
	RoomID           string `gorm:"column:room_id;type:varchar(64);not null;index:idx_lot_stats_room;index:idx_lot_stats_main_room,priority:2"`
	BidCount         int64  `gorm:"column:bid_count;not null;default:0"`
	ParticipantCount int64  `gorm:"column:participant_count;not null;default:0"`
	LastBidID        string `gorm:"column:last_bid_id;type:varchar(64);not null;default:''"`
	LastBidAtUnixMs  int64  `gorm:"column:last_bid_at_unix_ms;not null;default:0"`
	ProjectedVersion int64  `gorm:"column:projected_version;not null;default:0"`
	UpdatedAtUnixMs  int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AuctionLotStatsModel) TableName() string { return "auction_lot_stats" }

type AuctionLotParticipantModel struct {
	LotID            string `gorm:"column:lot_id;type:varchar(64);primaryKey"`
	UserID           string `gorm:"column:user_id;type:varchar(64);primaryKey"`
	MainAccountID    string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_lot_participants_main_room,priority:1"`
	RoomID           string `gorm:"column:room_id;type:varchar(64);not null;index:idx_lot_participants_room;index:idx_lot_participants_main_room,priority:2"`
	FirstBidID       string `gorm:"column:first_bid_id;type:varchar(64);not null"`
	FirstBidAtUnixMs int64  `gorm:"column:first_bid_at_unix_ms;not null"`
	CreatedAt        time.Time
}

func (AuctionLotParticipantModel) TableName() string { return "auction_lot_participants" }

type AuctionEventModel struct {
	ID               string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID    string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_event_main_room_occurred,priority:1"`
	RoomID           string `gorm:"column:room_id;type:varchar(64);not null;index:idx_room_occurred,priority:1;index:idx_event_main_room_occurred,priority:2"`
	LotID            string `gorm:"column:lot_id;type:varchar(64);not null;default:'';index:idx_lot_occurred,priority:1"`
	Type             int32  `gorm:"column:type;type:int;not null;index:idx_type_occurred,priority:1"`
	OccurredAtUnixMs int64  `gorm:"column:occurred_at_unix_ms;not null;index:idx_room_occurred,priority:2;index:idx_lot_occurred,priority:2;index:idx_type_occurred,priority:2;index:idx_event_main_room_occurred,priority:3"`
	Reason           string `gorm:"column:reason;type:varchar(512);not null;default:''"`
	Payload          string `gorm:"column:payload;type:json;not null"`
	CreatedAt        time.Time
}

func (AuctionEventModel) TableName() string { return "auction_events" }

type UserOrderModel struct {
	ID                      string `gorm:"column:id;type:varchar(64);primaryKey"`
	Source                  string `gorm:"column:source;type:varchar(32);not null;uniqueIndex:uk_user_order_source_id,priority:1;index:idx_user_order_user_status,priority:3;index:idx_user_order_source_created,priority:1"`
	SourceOrderID           string `gorm:"column:source_order_id;type:varchar(64);not null;uniqueIndex:uk_user_order_source_id,priority:2"`
	OrderNo                 string `gorm:"column:order_no;type:varchar(64);not null;default:'';index:idx_user_order_no"`
	MainAccountID           string `gorm:"column:main_account_id;type:varchar(64);not null;default:'';index:idx_user_order_main_created,priority:1"`
	UserID                  string `gorm:"column:user_id;type:varchar(64);not null;index:idx_user_order_user_status,priority:1"`
	Nickname                string `gorm:"column:nickname;type:varchar(128);not null;default:''"`
	Status                  string `gorm:"column:status;type:varchar(32);not null;index:idx_user_order_user_status,priority:2"`
	PaymentStatus           string `gorm:"column:payment_status;type:varchar(32);not null;index:idx_user_order_payment_status"`
	PaymentID               string `gorm:"column:payment_id;type:varchar(64);not null;default:''"`
	Title                   string `gorm:"column:title;type:varchar(255);not null;default:''"`
	ShopName                string `gorm:"column:shop_name;type:varchar(128);not null;default:''"`
	TotalAmount             int64  `gorm:"column:total_amount;not null"`
	Currency                string `gorm:"column:currency;type:varchar(16);not null;default:'CNY'"`
	ShippingAddressID       string `gorm:"column:shipping_address_id;type:varchar(64);not null;default:'';index:idx_user_order_shipping_address"`
	ShippingAddressSnapshot string `gorm:"column:shipping_address_snapshot;type:json"`
	AddressSnapshot         string `gorm:"column:address_snapshot;type:varchar(512);not null;default:''"`
	CreatedAtUnixMs         int64  `gorm:"column:created_at_unix_ms;not null;index:idx_user_order_created;index:idx_user_order_main_created,priority:2;index:idx_user_order_source_created,priority:2"`
	UpdatedAtUnixMs         int64  `gorm:"column:updated_at_unix_ms;not null"`
	PaidAtUnixMs            int64  `gorm:"column:paid_at_unix_ms;not null;default:0"`
	ExpiresAtUnixMs         int64  `gorm:"column:expires_at_unix_ms;not null;default:0;index:idx_user_order_expiry"`
	Version                 int64  `gorm:"column:version;not null;default:1"`
	PaymentIdempotencyKey   string `gorm:"column:payment_idempotency_key;type:varchar(128);not null;default:''"`
	SourcePayload           string `gorm:"column:source_payload;type:json"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (UserOrderModel) TableName() string { return "user_orders" }

type AuctionOrderEnrichmentModel struct {
	OrderID         string `gorm:"column:order_id;type:varchar(64);primaryKey"`
	SourceMessageID string `gorm:"column:source_message_id;type:varchar(128);not null;uniqueIndex:uk_source_message"`
	PayloadHash     string `gorm:"column:payload_hash;type:char(64);not null"`
	AddressSnapshot string `gorm:"column:address_snapshot;type:json"`
	ShopSnapshot    string `gorm:"column:shop_snapshot;type:json"`
	Status          string `gorm:"column:status;type:varchar(16);not null"`
	Attempts        int    `gorm:"column:attempts;not null"`
	LastError       string `gorm:"column:last_error;type:varchar(512);not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_ms;not null"`
}

func (AuctionOrderEnrichmentModel) TableName() string { return "auction_order_enrichments" }

type UserOrderItemModel struct {
	ID           string `gorm:"column:id;type:varchar(64);primaryKey"`
	OrderID      string `gorm:"column:order_id;type:varchar(64);not null;index:idx_user_order_item_order"`
	Source       string `gorm:"column:source;type:varchar(32);not null;index:idx_user_order_item_source"`
	SourceItemID string `gorm:"column:source_item_id;type:varchar(64);not null;default:''"`
	ProductID    string `gorm:"column:product_id;type:varchar(64);not null;default:'';index:idx_user_order_item_product"`
	SKUID        string `gorm:"column:sku_id;type:varchar(64);not null;default:''"`
	LotID        string `gorm:"column:lot_id;type:varchar(64);not null;default:'';index:idx_user_order_item_lot"`
	RoomID       string `gorm:"column:room_id;type:varchar(64);not null;default:'';index:idx_user_order_item_room"`
	Title        string `gorm:"column:title;type:varchar(255);not null;default:''"`
	ImageURL     string `gorm:"column:image_url;type:varchar(1024);not null;default:''"`
	SKUName      string `gorm:"column:sku_name;type:varchar(128);not null;default:''"`
	Quantity     int64  `gorm:"column:quantity;not null;default:1"`
	UnitAmount   int64  `gorm:"column:unit_amount;not null"`
	TotalAmount  int64  `gorm:"column:total_amount;not null"`
	Currency     string `gorm:"column:currency;type:varchar(16);not null;default:'CNY'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (UserOrderItemModel) TableName() string { return "user_order_items" }

type UserOrderPaymentModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	OrderID         string `gorm:"column:order_id;type:varchar(64);not null;index:idx_user_order_payment_order;uniqueIndex:uk_user_order_payment_idem,priority:1"`
	Source          string `gorm:"column:source;type:varchar(32);not null;index:idx_user_order_payment_source"`
	Provider        string `gorm:"column:provider;type:varchar(32);not null;default:'mock'"`
	MainAccountID   string `gorm:"column:main_account_id;type:varchar(64);not null;default:'';index:idx_user_order_payment_main_created,priority:1"`
	LotID           string `gorm:"column:lot_id;type:varchar(64);not null;default:'';index:idx_user_order_payment_lot"`
	UserID          string `gorm:"column:user_id;type:varchar(64);not null;index:idx_user_order_payment_user"`
	Status          string `gorm:"column:status;type:varchar(32);not null;index:idx_user_order_payment_status"`
	Amount          int64  `gorm:"column:amount;not null"`
	Currency        string `gorm:"column:currency;type:varchar(16);not null;default:'CNY'"`
	IdempotencyKey  string `gorm:"column:idempotency_key;type:varchar(128);not null;uniqueIndex:uk_user_order_payment_idem,priority:2"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null;index:idx_user_order_payment_created;index:idx_user_order_payment_main_created,priority:2"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null;default:0"`
	SucceededAtMs   int64  `gorm:"column:succeeded_at_unix_ms;not null;default:0"`
	SourcePayload   string `gorm:"column:source_payload;type:json"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (UserOrderPaymentModel) TableName() string { return "user_order_payments" }

type AuctionDepositHoldModel struct {
	ID               string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID    string `gorm:"column:main_account_id;type:varchar(64);not null;index:idx_deposit_main_created,priority:1"`
	RoomID           string `gorm:"column:room_id;type:varchar(64);not null;index:idx_deposit_room"`
	LotID            string `gorm:"column:lot_id;type:varchar(64);not null;index:idx_deposit_lot_status,priority:1;uniqueIndex:uk_deposit_lot_buyer,priority:1;uniqueIndex:uk_deposit_idem,priority:1"`
	BuyerUserID      string `gorm:"column:buyer_user_id;type:varchar(64);not null;index:idx_deposit_buyer_status,priority:1;uniqueIndex:uk_deposit_lot_buyer,priority:2;uniqueIndex:uk_deposit_idem,priority:2"`
	BuyerNickname    string `gorm:"column:buyer_nickname;type:varchar(128);not null;default:''"`
	Status           string `gorm:"column:status;type:varchar(32);not null;index:idx_deposit_lot_status,priority:2;index:idx_deposit_buyer_status,priority:2"`
	Amount           int64  `gorm:"column:amount;not null"`
	Currency         string `gorm:"column:currency;type:varchar(16);not null"`
	PaymentProvider  string `gorm:"column:payment_provider;type:varchar(32);not null;default:'mock'"`
	PaymentID        string `gorm:"column:payment_id;type:varchar(64);not null;default:''"`
	IdempotencyKey   string `gorm:"column:idempotency_key;type:varchar(128);not null;uniqueIndex:uk_deposit_idem,priority:3"`
	AddressID        string `gorm:"column:address_id;type:varchar(64);not null;default:''"`
	AddressSnapshot  string `gorm:"column:address_snapshot;type:json"`
	CreatedAtUnixMs  int64  `gorm:"column:created_at_unix_ms;not null;index:idx_deposit_main_created,priority:2"`
	UpdatedAtUnixMs  int64  `gorm:"column:updated_at_unix_ms;not null"`
	HeldAtUnixMs     int64  `gorm:"column:held_at_unix_ms;not null;default:0"`
	ReleasedAtUnixMs int64  `gorm:"column:released_at_unix_ms;not null;default:0"`
	Payload          string `gorm:"column:payload;type:json;not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AuctionDepositHoldModel) TableName() string { return "auction_deposit_holds" }

type ShopProductModel struct {
	ID                  string `gorm:"column:id;type:varchar(64);primaryKey"`
	Title               string `gorm:"column:title;type:varchar(255);not null"`
	Subtitle            string `gorm:"column:subtitle;type:varchar(512);not null;default:''"`
	Description         string `gorm:"column:description;type:text;not null"`
	Category            string `gorm:"column:category;type:varchar(64);not null;index:idx_shop_product_category"`
	ShopName            string `gorm:"column:shop_name;type:varchar(128);not null"`
	MainImageURL        string `gorm:"column:main_image_url;type:varchar(1024);not null"`
	DetailImageURLs     string `gorm:"column:detail_image_urls;type:json;not null"`
	Tags                string `gorm:"column:tags;type:json;not null"`
	Badges              string `gorm:"column:badges;type:json;not null"`
	PriceAmount         int64  `gorm:"column:price_amount;not null"`
	OriginalPriceAmount int64  `gorm:"column:original_price_amount;not null;default:0"`
	Currency            string `gorm:"column:currency;type:varchar(16);not null;default:'CNY'"`
	SoldLabel           string `gorm:"column:sold_label;type:varchar(64);not null;default:''"`
	Live                bool   `gorm:"column:live;not null;default:false"`
	Status              string `gorm:"column:status;type:varchar(32);not null;default:'active';index:idx_shop_product_status"`
	CreatedAtUnixMs     int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs     int64  `gorm:"column:updated_at_unix_ms;not null;index:idx_shop_product_updated"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (ShopProductModel) TableName() string { return "shop_products" }

type ShopSKUModel struct {
	ID          string `gorm:"column:id;type:varchar(64);primaryKey"`
	ProductID   string `gorm:"column:product_id;type:varchar(64);not null;index:idx_shop_sku_product"`
	Name        string `gorm:"column:name;type:varchar(128);not null"`
	PriceAmount int64  `gorm:"column:price_amount;not null"`
	Currency    string `gorm:"column:currency;type:varchar(16);not null;default:'CNY'"`
	Stock       int64  `gorm:"column:stock;not null;default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (ShopSKUModel) TableName() string { return "shop_skus" }

type UserDeliveryAddressModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	UserID          string `gorm:"column:user_id;type:varchar(64);not null;index:idx_user_address_active,priority:1"`
	ReceiverName    string `gorm:"column:receiver_name;type:varchar(64);not null"`
	Phone           string `gorm:"column:phone;type:varchar(32);not null"`
	Province        string `gorm:"column:province;type:varchar(64);not null;default:''"`
	City            string `gorm:"column:city;type:varchar(64);not null;default:''"`
	District        string `gorm:"column:district;type:varchar(64);not null;default:''"`
	Street          string `gorm:"column:street;type:varchar(128);not null;default:''"`
	Detail          string `gorm:"column:detail;type:varchar(512);not null"`
	PostalCode      string `gorm:"column:postal_code;type:varchar(32);not null;default:''"`
	Tag             string `gorm:"column:tag;type:varchar(32);not null;default:''"`
	IsDefault       bool   `gorm:"column:is_default;not null;default:false"`
	Status          string `gorm:"column:status;type:varchar(32);not null;default:'active';index:idx_user_address_active,priority:2"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null;index:idx_user_address_active,priority:3"`
	DeletedAtUnixMs int64  `gorm:"column:deleted_at_unix_ms;not null;default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (UserDeliveryAddressModel) TableName() string { return "user_delivery_addresses" }

type AuctionUserModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	Username        string `gorm:"column:username;type:varchar(64);not null;uniqueIndex:idx_username"`
	Nickname        string `gorm:"column:nickname;type:varchar(128);not null"`
	AvatarURL       string `gorm:"column:avatar_url;type:varchar(512);not null;default:''"`
	PasswordHash    string `gorm:"column:password_hash;type:varchar(255);not null"`
	MainAccountID   string `gorm:"column:main_account_id;type:varchar(64);not null;default:'';index:idx_user_main_status,priority:1"`
	CreatedByUserID string `gorm:"column:created_by_user_id;type:varchar(64);not null;default:'';index:idx_user_created_by"`
	Status          int32  `gorm:"column:status;type:int;not null;default:1;index:idx_user_main_status,priority:2;index:idx_user_status"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionUserModel) TableName() string { return "auction_users" }

type AuctionRoleModel struct {
	Code            string `gorm:"column:code;type:varchar(64);primaryKey"`
	Name            string `gorm:"column:name;type:varchar(128);not null"`
	Description     string `gorm:"column:description;type:varchar(512);not null;default:''"`
	System          bool   `gorm:"column:system;not null;default:true"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionRoleModel) TableName() string { return "auction_roles" }

type AuctionPermissionModel struct {
	Code            string `gorm:"column:code;type:varchar(96);primaryKey"`
	Name            string `gorm:"column:name;type:varchar(128);not null"`
	Module          string `gorm:"column:module;type:varchar(64);not null;index:idx_permission_module"`
	Description     string `gorm:"column:description;type:varchar(512);not null;default:''"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	UpdatedAtUnixMs int64  `gorm:"column:updated_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionPermissionModel) TableName() string { return "auction_permissions" }

type AuctionUserRoleModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	UserID          string `gorm:"column:user_id;type:varchar(64);not null;uniqueIndex:uk_user_role_scope,priority:1;index:idx_user_role_user"`
	RoleCode        string `gorm:"column:role_code;type:varchar(64);not null;uniqueIndex:uk_user_role_scope,priority:2;index:idx_user_role_role"`
	MainAccountID   string `gorm:"column:main_account_id;type:varchar(64);not null;default:'';uniqueIndex:uk_user_role_scope,priority:3;index:idx_user_role_main"`
	GrantedByUserID string `gorm:"column:granted_by_user_id;type:varchar(64);not null;default:''"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionUserRoleModel) TableName() string { return "auction_user_roles" }

type AuctionRolePermissionModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	RoleCode        string `gorm:"column:role_code;type:varchar(64);not null;uniqueIndex:uk_role_permission,priority:1;index:idx_role_permission_role"`
	PermissionCode  string `gorm:"column:permission_code;type:varchar(96);not null;uniqueIndex:uk_role_permission,priority:2;index:idx_role_permission_permission"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionRolePermissionModel) TableName() string { return "auction_role_permissions" }

type AuctionUserPermissionModel struct {
	ID              string `gorm:"column:id;type:varchar(64);primaryKey"`
	UserID          string `gorm:"column:user_id;type:varchar(64);not null;uniqueIndex:uk_user_permission,priority:1;index:idx_user_permission_user"`
	PermissionCode  string `gorm:"column:permission_code;type:varchar(96);not null;uniqueIndex:uk_user_permission,priority:2;index:idx_user_permission_permission"`
	Effect          string `gorm:"column:effect;type:varchar(16);not null;default:'allow'"`
	GrantedByUserID string `gorm:"column:granted_by_user_id;type:varchar(64);not null;default:''"`
	CreatedAtUnixMs int64  `gorm:"column:created_at_unix_ms;not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (AuctionUserPermissionModel) TableName() string { return "auction_user_permissions" }

type AuctionUserSessionModel struct {
	ID                     string `gorm:"column:id;type:varchar(64);primaryKey"`
	UserID                 string `gorm:"column:user_id;type:varchar(64);not null;index:idx_user_sessions"`
	RefreshTokenHash       string `gorm:"column:refresh_token_hash;type:varchar(64);not null;uniqueIndex:idx_refresh_token_hash"`
	RefreshExpiresAtUnixMs int64  `gorm:"column:refresh_expires_at_unix_ms;not null;index:idx_session_expiry"`
	RevokedAtUnixMs        int64  `gorm:"column:revoked_at_unix_ms;not null;default:0"`
	CreatedAtUnixMs        int64  `gorm:"column:created_at_unix_ms;not null"`
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (AuctionUserSessionModel) TableName() string { return "auction_user_sessions" }

type AssetFileModel struct {
	ID               string `gorm:"column:id;type:varchar(64);primaryKey"`
	MainAccountID    string `gorm:"column:main_account_id;type:varchar(64);not null;default:'';index:idx_asset_main_room,priority:1"`
	OwnerUserID      string `gorm:"column:owner_user_id;type:varchar(64);not null;index:idx_asset_owner"`
	RoomID           string `gorm:"column:room_id;type:varchar(64);not null;default:'';index:idx_asset_room;index:idx_asset_main_room,priority:2"`
	BizType          string `gorm:"column:biz_type;type:varchar(64);not null;index:idx_asset_biz_type"`
	Status           string `gorm:"column:status;type:varchar(32);not null;default:'temporary';index:idx_asset_status"`
	AttachedLotID    string `gorm:"column:attached_lot_id;type:varchar(64);not null;default:'';index:idx_asset_attached_lot"`
	StorageProvider  string `gorm:"column:storage_provider;type:varchar(32);not null"`
	Bucket           string `gorm:"column:bucket;type:varchar(128);not null"`
	ObjectKey        string `gorm:"column:object_key;type:varchar(512);not null;uniqueIndex:idx_asset_object_key"`
	PublicURL        string `gorm:"column:public_url;type:varchar(1024);not null"`
	OriginalName     string `gorm:"column:original_name;type:varchar(255);not null;default:''"`
	MimeType         string `gorm:"column:mime_type;type:varchar(64);not null"`
	SizeBytes        int64  `gorm:"column:size_bytes;not null"`
	SHA256           string `gorm:"column:sha256;type:char(64);not null;index:idx_asset_sha256"`
	AttachedAtUnixMs int64  `gorm:"column:attached_at_unix_ms;not null;default:0"`
	DeletedAtUnixMs  int64  `gorm:"column:deleted_at_unix_ms;not null;default:0;index:idx_asset_deleted_at"`
	ExpiresAtUnixMs  int64  `gorm:"column:expires_at_unix_ms;not null;default:0;index:idx_asset_expiry"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AssetFileModel) TableName() string { return "asset_files" }
