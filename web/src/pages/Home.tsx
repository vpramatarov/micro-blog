// Home is the landing page: it links to the public (anonymous) API endpoints and
// also doubles as an API connectivity smoke test by listing published post titles
// from GET /posts. An empty list (fresh DB) is fine — it still proves the SPA is
// served by Go and can reach the API.

import { useEffect, useState } from "react";
import { listPosts } from "../lib/api";
import type { Post } from "../types";
import { uploadsUrl } from "../lib/uploads";
import { Link } from "react-router-dom";

// Directly GET-able public endpoints (rendered as links).
const PUBLIC_LINKS: { href: string; label: string }[] = [
  { href: "/posts", label: "Published posts (JSON)" },
  { href: "/categories", label: "Categories (JSON)" },
  { href: "/tags", label: "Tags (JSON)" },
  { href: "/docs", label: "Swagger UI — interactive API docs" },
  { href: "/openapi.json", label: "OpenAPI spec (JSON)" },
  { href: "/openapi.yaml", label: "OpenAPI spec (YAML)" },
];

// Public endpoints that take a path parameter (shown as patterns, not links).
const PUBLIC_PATTERNS: string[] = [
  "GET /posts/{slug} — read one post by slug",
  "GET /p/{code} — read one post by hashid",
  "GET /categories/{slug} — posts in a category",
  "GET /tags/{slug} — posts with a tag",
  "GET /s/{code} — resolve a short link",
];

export function Home() {
  const [posts, setPosts] = useState<Post[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const POSTS_BASE = "/posts/";

  useEffect(() => {
    let cancelled = false;
    listPosts()
      .then((page) => {
        if (!cancelled) setPosts(page.items);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "failed to load posts");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <section class="max-w-5xl mx-auto py-12 px-6">
      <h1 class="text-5xl font-bold mb-4">Blog</h1>
      {/*<p>Markdown micro-blog API with a built-in URL shortener. Public endpoints:</p>*/}

      {/*<h2>Explore</h2>*/}
      {/*<ul>*/}
      {/*  {PUBLIC_LINKS.map((l) => (*/}
      {/*    <li key={l.href}>*/}
      {/*      <a href={l.href}>{l.href}</a> — {l.label}*/}
      {/*    </li>*/}
      {/*  ))}*/}
      {/*</ul>*/}

      {/*<h2>Parameterized</h2>*/}
      {/*<ul>*/}
      {/*  {PUBLIC_PATTERNS.map((p) => (*/}
      {/*    <li key={p}>*/}
      {/*      <code>{p}</code>*/}
      {/*    </li>*/}
      {/*  ))}*/}
      {/*</ul>*/}

      <h2 class="text-2xl font-semibold mb-2">Latest posts</h2>
      {loading && <p>Loading…</p>}
      {error && <p className="error">Could not load posts: {error}</p>}
      {!loading && !error && posts.length === 0 && <p>No published posts yet.</p>}
      <ul>
        {posts.map((p) => (

              <li key={p.id} class="mb-5">
                <article className="bg-white rounded-2xl shadow-card p-8 hover:shadow-xl transition">
                  {p.featured_image_path ? (
                      <Link to={POSTS_BASE + p.slug}>
                        <img src={uploadsUrl(p.featured_image_path, "s")} alt={p.title}/>
                      </Link>
                  ) : null}

                  <h2 class="text-3xl font-bold"><Link className="hover:text-primary"
                                                       to={POSTS_BASE + p.slug}>{p.title}</Link> <small
                      class="block text-gray-500 text-sm font-normal mt-1">by {p.author_name}</small></h2>
                  <p class="mt-5 text-gray-600 leading-7">{p.excerpt}</p>

                  <div class="mt-8 flex justify-between items-center">

                    <div class="text-sm text-gray-500 flex gap-5">
                      <span>📅 May 18, 2024</span>
                      <span>• 2 min read</span>
                    </div>

                    <Link to={POSTS_BASE + p.slug} className="font-semibold text-primary hover:underline">
                      Read more →
                    </Link>

                  </div>

                </article>
              </li>

        ))}
      </ul>
    </section>
  );
}
