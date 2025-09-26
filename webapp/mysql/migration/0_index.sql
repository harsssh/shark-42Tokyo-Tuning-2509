-- このファイルに記述されたSQLコマンドが、マイグレーション時に実行されます。
ALTER TABLE products ADD FULLTEXT INDEX ft_idx_products_name_description (name, description) WITH PARSER ngram;
