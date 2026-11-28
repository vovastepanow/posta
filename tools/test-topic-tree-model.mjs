import assert from 'node:assert/strict';
import { buildTopicTree, treeNorm, topicTags } from '../js/topic-tree-model.js';

const topics = [
    {id:'root',title:'Маркетинг',scope:'broad'},
    {id:'b',title:'Яндекс',parent:{id:'root'},hashtags:{rubric:'реклама',entities:['Таргет'],synonyms:['контекст']}},
    {id:'a',title:'Блогинг',parent:{id:'root'},aliases:['SMM и блогинг']},
    {id:'child',title:'Ёлки',parent:{id:'a'}},
    {id:'orphan',title:'Другие',parent:{id:'missing'}},
    {id:'cycle-a',title:'A',parent:{id:'cycle-b'}},
    {id:'cycle-b',title:'B',parent:{id:'cycle-a'}}
];
const model = buildTopicTree(topics);
assert.deepEqual(model.rubrics.map(t=>t.id), ['root','orphan']);
assert.deepEqual(model.descendants(topics[0]).map(t=>t.id), ['root','a','child','b']);
assert.equal(model.descendants(topics[0]),model.descendants(topics[0]));
assert.equal(model.rubricOf(topics[3]),topics[0]);
assert.equal(model.rubricOf(topics[3]),topics[0]);
assert.equal(model.rubricOf(topics[4]),topics[4]);
assert.equal(model.descendants(topics[5]).length,2);
assert.ok(model.rubricOf(topics[5]));
assert.ok(model.matches(topics[2],treeNorm('SMM-и блогинг')));
assert.ok(model.matches(topics[3],treeNorm('#елки')));
assert.ok(model.matches(topics[1],treeNorm('#таргет')));
assert.ok(model.matches(topics[1],treeNorm('контекст')));
assert.ok(!model.matches(topics[1],'unrelated'));
assert.deepEqual(topicTags(topics[1]),['реклама','Таргет']);
console.log('Topic index, ordering, cycles and search passed.');
