-- +goose Up
 INSERT INTO roles (id, `name`) VALUES (1, 'Admin'), (2, 'Editor'), (3, 'Author'), (4, 'Subscriber');

 INSERT INTO permissions (id, `name`) VALUES
     (1, 'post:create'), (2, 'post:edit'), (3, 'post:delete'), (4, 'post:view'),
     (5, 'shortlink:create'), (6, 'shortlink:edit'), (7, 'shortlink:delete'),
     (8, 'user:view'), (9, 'user:edit'), (10, 'user:delete');

 -- Role/permission/scope wiring (Admin gets 'all' on everything; Author/Editor/Subscriber scoped)
 INSERT INTO role_permissions (role_id, permission_id, scope) VALUES
     -- Admin: all permissions with scope='all'
     (1,1,'all'),(1,2,'all'),(1,3,'all'),(1,4,'all'),(1,5,'all'),(1,6,'all'),(1,7,'all'),(1,8,'all'),(1,9,'all'),(1,10,
 'all'),
     -- Editor: full post control, own shortlinks, own profile
     (2,1,'all'),(2,2,'all'),(2,3,'all'),(2,4,'all'),(2,5,'all'),(2,6,'own'),(2,7,'own'),(2,8,'own'),(2,9,'own'),
     -- Author: own posts, own shortlinks, own profile, can view all posts
     (3,1,'all'),(3,2,'own'),(3,3,'own'),(3,4,'all'),(3,5,'all'),(3,6,'own'),(3,7,'own'),(3,8,'own'),(3,9,'own'),
     -- Subscriber: view posts, own profile
     (4,4,'all'),(4,8,'own'),(4,9,'own');

 -- Add role_id to users (default Subscriber=4 so any pre-existing row is non-NULL)
 ALTER TABLE users ADD COLUMN role_id INTEGER NOT NULL DEFAULT 4 REFERENCES roles(id);

-- +goose Down
 ALTER TABLE users DROP COLUMN role_id;
 DELETE FROM role_permissions;
 DELETE FROM permissions;
 DELETE FROM roles;