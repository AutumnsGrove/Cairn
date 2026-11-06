<script lang="ts">
  // S00 renders one honest page. The logging flow, the library and the entry card arrive in S10 to S12, and
  // they are built against the mockups, not against this placeholder.
</script>

<svelte:head>
  <title>Sepia</title>
  <meta name="description" content="A private review journal for anything you finish." />
</svelte:head>

<main>
  <div class="mark" aria-hidden="true"></div>
  <h1>Sepia</h1>
  <p class="lede">
    A private journal for everything you finish. Films, TV, books, games, albums and more.
  </p>
  <p class="state">
    The app is being built. This is the walking skeleton: it serves this page and answers a health
    check.
  </p>
</main>

<style>
  main {
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 0.75rem;
    max-width: 34rem;
    padding: 2rem max(1.25rem, env(safe-area-inset-left)) 4rem;
    margin: 0 auto;
  }

  /* One flat accent bar, a stroke of ink rather than an illustration: the name is the ink, not a
     filter, so the mark stays a single shape. */
  .mark {
    width: 2.5rem;
    height: 0.25rem;
    border-radius: var(--r-sm);
    background: var(--accent);
    margin-bottom: 1rem;
  }

  h1 {
    font-family: 'Fraunces', Georgia, serif;
    font-weight: 400;
    font-size: clamp(2.5rem, 12vw, 3.5rem);
    line-height: 1.05;
    letter-spacing: -0.01em;
    margin: 0;
  }

  .lede {
    font-size: 1.125rem;
    line-height: 1.6;
    color: var(--text-2);
    margin: 0;
    max-width: 28ch;
  }

  .state {
    margin: 1.5rem 0 0;
    padding-top: 1.25rem;
    border-top: 1px solid var(--line);
    color: var(--text-3);
    font-size: 0.9375rem;
    max-width: 34ch;
  }
</style>
