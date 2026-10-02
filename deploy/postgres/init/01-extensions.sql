-- 数据库初始化:扩展。
--
-- pgcrypto 提供 gen_random_uuid() 与 digest(),迁移文件依赖它们。
-- PostgreSQL 13 起 gen_random_uuid() 已内置,但 pgcrypto 里的
-- digest() 仍然是部分查询的依赖,所以照装。

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
