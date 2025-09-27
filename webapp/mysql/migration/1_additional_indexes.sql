-- 追加のインデックス

-- shipped_status 単独でのインデックス（ロボット配送計画用）
ALTER TABLE orders
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_shipped_status (shipped_status);

-- 注文履歴のソート用インデックス
ALTER TABLE orders
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_created_at (created_at);

-- 商品のソート用インデックス
ALTER TABLE products
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_value (value),
    ADD INDEX idx_weight (weight);

-- 注文履歴のソート・検索用複合インデックス
ALTER TABLE orders
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_user_created_at (user_id, created_at),
    ADD INDEX idx_user_shipped_status (user_id, shipped_status),
    ADD INDEX idx_user_arrived_at (user_id, arrived_at);

-- 商品検索・ソート用複合インデックス
ALTER TABLE products
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_name_product_id (name, product_id),
    ADD INDEX idx_value_product_id (value, product_id),
    ADD INDEX idx_weight_product_id (weight, product_id);