ALTER TABLE products
    ADD INDEX idx_products_value_product_id (value, product_id),
    ADD INDEX idx_products_weight_product_id (weight, product_id),
    ADD INDEX idx_products_name_product_id (name, product_id),
    ADD INDEX idx_products_name (name),
    ADD INDEX idx_products_product_id_weight_value (product_id, weight, value);

-- ログインの改善
ALTER TABLE users ADD INDEX idx_users_user_name (user_name);

-- delivery plan を作るときの orders の join に使う (covering index)
ALTER TABLE orders ADD INDEX idx_orders_shipped_status_product_id_order_id (shipped_status, product_id, order_id);
