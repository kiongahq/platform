// Public blog pages: author-controlled fields are escaped, only the server's
// sanitized rendered_html is inserted as markup, filters and author pages work.
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const {JSDOM} = require('jsdom');
const tick = () => new Promise(resolve => setTimeout(resolve, 40));
const WEB = 'go/cmd/gateway/web';
const payload = '<img src=x onerror="window.pwned=1">';

function page(file, url, data) {
  const dom = new JSDOM(fs.readFileSync(`${WEB}/${file}`, 'utf8'), {url, runScripts: 'outside-only', pretendToBeVisual: true});
  dom.window.fetch = async path => ({ok: !String(path).includes('missing'), json: async () => data(String(path))});
  dom.window.eval(fs.readFileSync(`${WEB}/blog.js`, 'utf8'));
  return dom;
}

const post = (overrides = {}) => ({id: 'p1', slug: 'gateway', title: 'Gateway', summary: 'How it works', author: 'Ana', tags: ['Go', 'Security'], created_at: '2026-01-01T00:00:00Z', published_at: '2026-01-02T00:00:00Z', reading_minutes: 4, ...overrides});

test('article escapes metadata and inserts only rendered_html as markup', async t => {
  const dom = page('blog.html', 'http://localhost/blog.html?slug=gateway', () => post({
    title: payload, summary: payload, author: payload, tags: [payload],
    cover: {url: '/media/blog/med-abcdef12/960', srcset: '/media/blog/med-abcdef12/480 480w', alt: `"${payload}`, width: 960, height: 480},
    rendered_html: '<p>Body <b>bold</b></p><figure class="kb-image"><img src="/media/blog/med-abcdef12/960" alt="Diagram"></figure>',
    related: [post({slug: 'other', title: payload})],
  }));
  t.after(() => dom.window.close());
  await tick();
  const doc = dom.window.document;
  assert.equal(doc.querySelector('#article-header img'), null, 'title/summary/tags/author markup rendered');
  assert.match(doc.querySelector('#article-header h1').textContent, /<img src=x/);
  assert.equal(doc.querySelector('#article-content b').textContent, 'bold');
  assert.equal(doc.querySelector('#article-content img').getAttribute('alt'), 'Diagram');
  const cover = doc.querySelector('#article-cover img');
  assert.equal(cover.getAttribute('alt'), `"${payload}`);
  assert.match(cover.getAttribute('srcset'), /480w/);
  assert.equal(doc.querySelector('#article-related h2 a').textContent, payload);
  assert.equal(doc.querySelector('.article-byline a').getAttribute('href'), `/blogs.html?author=${encodeURIComponent(payload)}`);
  assert.equal(dom.window.pwned, undefined);
});

test('missing article shows a not-found message', async t => {
  const dom = page('blog.html', 'http://localhost/blog.html?slug=missing', () => ({}));
  t.after(() => dom.window.close());
  await tick();
  assert.match(dom.window.document.querySelector('#article-header').textContent, /not found/);
});

test('index supports search, tag filter and author pages', async t => {
  const items = [post(), post({id: 'p2', slug: 's3', title: 'Mounting S3', summary: 'FUSE notes', author: 'Ben', tags: ['Storage']}), post({id: 'p3', slug: 'x', title: payload, author: 'Ana', tags: ['Go']})];
  const dom = page('blogs.html', 'http://localhost/blogs.html?author=Ana', () => ({items, site: {title: 'Custom headline', description: 'Desc'}}));
  t.after(() => dom.window.close());
  await tick();
  const doc = dom.window.document;
  assert.equal(doc.querySelector('#blog-headline').textContent, 'Custom headline');
  assert.equal(doc.querySelectorAll('#blog-index article').length, 2, 'author page lists only that author');
  assert.match(doc.querySelector('#blog-author-banner').textContent, /Posts by Ana/);
  assert.equal(doc.querySelector('#blog-index img'), null, 'escaped titles');
  doc.querySelector('[data-clear-author]').click();
  assert.equal(doc.querySelectorAll('#blog-index article').length, 3);
  doc.querySelector('[data-blog-tag="Storage"]').click();
  assert.equal(doc.querySelectorAll('#blog-index article').length, 1);
  assert.match(dom.window.location.search, /tag=Storage/);
  doc.querySelector('[data-blog-tag=""]').click();
  const search = doc.querySelector('#blog-search');
  search.value = 'fuse';
  search.dispatchEvent(new dom.window.Event('input'));
  await new Promise(resolve => setTimeout(resolve, 200));
  assert.equal(doc.querySelectorAll('#blog-index article').length, 1);
  assert.match(doc.querySelector('#blog-count').textContent, /1 post$/);
});

test('blog pages pin highlight.js integrity and the console no longer edits Markdown', () => {
  const blog = fs.readFileSync(`${WEB}/blog.html`, 'utf8');
  assert.match(blog, /type="importmap"/);
  const consoleHTML = fs.readFileSync(`${WEB}/console.html`, 'utf8');
  assert.doesNotMatch(consoleHTML, /blog-editor-dialog|Article Markdown/);
  assert.match(consoleHTML, /href="\/editorial.html"/);
});
