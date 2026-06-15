import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Post } from "../types";
import { ApiError, getPost } from "../lib/api";
import { NotFound } from "./NotFound";
import { uploadsUrl } from "../lib/uploads";

export function PostDetail() {
    const { slug } = useParams<{slug: string}>();
    const [post, setPost] = useState<Post | null>(null);
    const [error, setError] = useState<Error | null>(null);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        if (!slug) {
            return;
        }

        let cancelled = false;
        setLoading(true);
        setError(null);
        getPost(slug)
            .then((p) => {
                if (!cancelled) {
                    setPost(p);
                }
            }).
            catch((e: unknown) => {
                if (!cancelled) {
                    let err = e instanceof Error ? e : new Error("failed to load post.");
                    setError(err)
                }
            })
            .finally(() => {
                if (!cancelled) {
                    setLoading(false)
                }
            });

        return () => {
            cancelled = true;
        };
    }, [slug]);

    if (loading) {
        return <p>Loading...</p>
    }

    if (error instanceof ApiError && error.status === 404) {
        return <NotFound />
    }

    if (error) {
        return <p className="error">Could not load post: {error.message}</p>
    }

    if (!post) {
        return <NotFound />
    }

    const createdAt = new Date(post.created_at).toLocaleDateString();
    const tags = Object.values(post.tags ?? {})

    return (
        <section>
            <p>
                <Link to="/">Back</Link>
            </p>
            <h1>{post.title}</h1>
            <p className="post-meta">
                by {post.author_name} . {createdAt} 
                {post.category_name ? <> . {post.category_name}</> : null}
            </p>
            {post.featured_image_path ? (
                <img className="post-image" src={ uploadsUrl(post.featured_image_path, "l") } alt={post.title} onError={(e) => {
                    // variants are generated async and can 404 briefly; fallback to the original.
                    const img = e.currentTarget;
                    if (img.dataset.fallback) {
                        return;
                    }

                    img.dataset.fallback = "1"
                    img.src = uploadsUrl(post.featured_image_path)
                }}
                />
            ) : null}
            <div className="post-content" dangerouslySetInnerHTML={{ __html: post.html_content ?? "" }} />
            {tags.length > 0 ? <p className="post-meta tags">Tags: {tags.join(", ")}</p> : null}
        </section>
    )

}

