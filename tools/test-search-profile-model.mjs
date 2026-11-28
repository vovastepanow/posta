import assert from 'node:assert/strict';
import { filterSearchProfiles } from '../js/search-profile-model.js';
const profiles = [{id:'a',title:'C++'},{id:'b',title:'C#'},{id:'c',title:'Мемы'}];
assert.deepEqual(filterSearchProfiles(profiles,'C++').map(p=>p.id),['a']);
assert.deepEqual(filterSearchProfiles(profiles,'C#').map(p=>p.id),['b']);
assert.equal(filterSearchProfiles(profiles,'','c')[0].id,'c');
assert.deepEqual(profiles.map(p=>p.id),['a','b','c']);
